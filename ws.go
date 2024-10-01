/*
 * Primary WebSocket routines
 *
 * Copyright (C) 2024  Runxi Yu <https://runxiyu.org>
 * SPDX-License-Identifier: AGPL-3.0-or-later
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

/*
 * The message format is a WebSocket message separated with spaces.
 * The contents of each field could contain anything other than spaces,
 * null bytes, carriage returns, and newlines. The first character of
 * each argument cannot be a colon. As an exception, the last argument may
 * contain spaces and the first character thereof may be a colon, if the
 * argument is prefixed with a colon. The colon used for the prefix is not
 * considered part of the content of the message. For example, in
 *
 *    SQUISH POP :cat purr!!
 *
 * the first field is "SQUISH", the second field is "POP", and the third
 * field is "cat purr!!".
 *
 * It is essentially an RFC 1459 IRC message without trailing CR-LF and
 * without prefixes. See section 2.3.1 of RFC 1459 for an approximate
 * BNF representation.
 *
 * The reason this was chosen instead of using protobuf etc. is that it
 * is simple to parse without external libraries, and it also happens to
 * be a format I'm very familiar with, having extensively worked with the
 * IRC protocol.
 */

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func writeText(ctx context.Context, c *websocket.Conn, msg string) error {
	err := c.Write(ctx, websocket.MessageText, []byte(msg))
	if err != nil {
		return fmt.Errorf("error writing to connection: %w", err)
	}
	return nil
}

/*
 * Handle requests to the WebSocket endpoint and establish a connection.
 * Authentication is handled here, but afterwards, the connection is really
 * handled in handleConn.
 */
func handleWs(w http.ResponseWriter, req *http.Request) {
	wsOptions := &websocket.AcceptOptions{
		Subprotocols: []string{"cca1"},
	} //exhaustruct:ignore
	c, err := websocket.Accept(
		w,
		req,
		wsOptions,
	)
	if err != nil {
		wstr(w, http.StatusBadRequest, "This endpoint only supports valid WebSocket connections.")
		return
	}
	defer func() {
		_ = c.CloseNow()
	}()

	/*
	 * TODO: Here we fetch the cookie from the HTTP headers. On browser's
	 * I've tested, creating WebSocket connections with JavaScript still
	 * passes httponly cookies in the upgrade request. I'm not sure if this
	 * is true for all browsers and it wasn't simple to find a spec for
	 * this.
	 */
	sessionCookie, err := req.Cookie("session")
	if errors.Is(err, http.ErrNoCookie) {
		err := writeText(req.Context(), c, "U")
		if err != nil {
			log.Println(err)
		}
		return
	} else if err != nil {
		err := writeText(req.Context(), c, "E :Error fetching cookie")
		if err != nil {
			log.Println(err)
		}
		return
	}

	var userID string
	var expr int

	err = db.QueryRow(
		req.Context(),
		"SELECT id, expr FROM users WHERE session = $1",
		sessionCookie.Value,
	).Scan(&userID, &expr)
	if errors.Is(err, pgx.ErrNoRows) {
		err := writeText(req.Context(), c, "U")
		if err != nil {
			log.Println(err)
		}
		return
	} else if err != nil {
		err := writeText(req.Context(), c, "E :Database error while selecting session")
		if err != nil {
			log.Println(err)
		}
		return
	}

	/*
	 * Now that we have an authenticated request, this WebSocket connection
	 * may be simply associated with the session and userID.
	 * TODO: There are various race conditions that could occur if one user
	 * creates multiple connections, with the same or different session
	 * cookies. The last situation could occur in normal use when a user
	 * opens multiple instances of the page in one browser, and is not
	 * unique to custom clients or malicious users. Some effort must be
	 * taken to ensure that each user may only have one connection at a
	 * time.
	 */
	err = handleConn(
		req.Context(),
		c,
		sessionCookie.Value,
		userID,
	)
	if err != nil {
		log.Printf("%v", err)
		return
	}
}

/*
 * Split an IRC-style message of type []byte into type []string where each
 * element is a complete argument. Generally, arguments are separated by
 * spaces, and an argument that begins with a ':' causes the rest of the
 * line to be treated as a single argument.
 */
func splitMsg(b *[]byte) []string {
	mar := make([]string, 0, config.Perf.MessageArgumentsCap)
	elem := make([]byte, 0, config.Perf.MessageBytesCap)
	for i, c := range *b {
		switch c {
		case ' ':
			if (*b)[i+1] == ':' {
				mar = append(mar, string(elem))
				mar = append(mar, string((*b)[i+2:]))
				goto endl
			}
			mar = append(mar, string(elem))
			elem = make([]byte, 0, config.Perf.MessageBytesCap)
		default:
			elem = append(elem, c)
		}
	}
	mar = append(mar, string(elem))
endl:
	return mar
}

func protocolError(ctx context.Context, conn *websocket.Conn, e string) error {
	err := writeText(ctx, conn, "E :"+e)
	if err != nil {
		return fmt.Errorf("error reporting protocol violation: %w", err)
	}
	err = conn.Close(websocket.StatusProtocolError, e)
	if err != nil {
		return fmt.Errorf("error closing websocket: %w", err)
	}
	return nil
}

type errbytesT struct {
	err   error
	bytes *[]byte
}

var (
	chanPool map[string](*chan string)
	/*
	 * Normal Go maps are not thread safe, so we protect large chanPool
	 * operations such as addition and deletion under a RWMutex.
	 */
	chanPoolLock sync.RWMutex
)

func setupChanPool() error {
	/*
	 * It would be unusual for this function to run concurrently with
	 * anything else that modifies chanPool, so we fail when the lock is
	 * unsuccessful.
	 */
	r := chanPoolLock.TryLock()
	if !r {
		return fmt.Errorf("cannot set up chanPool: %w", errUnexpectedRace)
	}
	defer chanPoolLock.Unlock()
	chanPool = make(map[string](*chan string))
	return nil
}

/*
 * Only call this when it is okay for propagation to fail, such as in course
 * number updates. Failures are currently ignored.
 */
func propagateIgnoreFailures(msg string) {
	/*
	 * It is not a mistake that we acquire a read lock instead of a write
	 * lock here. Channels provide synchronization, and other than using
	 * the channels, we are simply iterating through chanPoolLock. This is
	 * unsafe when chanPoolLock's structure is being modified, such as
	 * when a channel is being added or deleted from the pool; but it's
	 * fine if other goroutines are simply indexing it and using the
	 * channels.
	 */
	chanPoolLock.RLock()
	defer chanPoolLock.RUnlock()
	for k, v := range chanPool {
		select {
		case *v <- msg:
		default:
			log.Println("WARNING: SendQ exceeded for " + k)
			/* TODO: Perhaps it should be retried sometime */
		}
	}
	/* TODO: Any possible errors? */
}

/*
 * The actual logic in handling the connection, after authentication has been
 * completed.
 */
func handleConn(
	ctx context.Context,
	c *websocket.Conn,
	session string,
	userID string,
) error {
	/*
	 * TODO: Check for potential race conditions in chanPool handling
	 */
	send := make(chan string, config.Perf.SendQ)
	chanPoolLock.Lock()
	func() {
		defer chanPoolLock.Unlock()
		chanPool[session] = &send
		log.Printf("Channel %v added to pool for session %s, userID %s\n", &send, session, userID)
	}()
	defer func() {
		chanPoolLock.Lock()
		defer chanPoolLock.Unlock()
		delete(chanPool, session)
		log.Printf("Purging channel %v for session %s userID %s, from pool\n", &send, session, userID)
	}()

	/*
	 * Later we need to select from recv and send and perform the
	 * corresponding action. But we can't just select from c.Read because
	 * the function blocks. Therefore, we must spawn a goroutine that
	 * blocks on c.Read and send what it receives to a channel "recv"; and
	 * then we can select from that channel.
	 */
	recv := make(chan *errbytesT)
	go func() {
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				recv <- &errbytesT{err: err, bytes: nil}
				return
			}
			recv <- &errbytesT{err: nil, bytes: &b}
		}
	}()

	for {
		var mar []string
		select {
		case gonnasend := <-send:
			err := writeText(ctx, c, gonnasend)
			if err != nil {
				return fmt.Errorf("error sending to websocket from send channel: %w", err)
			}
			continue
		case errbytes := <-recv:
			if errbytes.err != nil {
				return errbytes.err
			}
			mar = splitMsg(errbytes.bytes)
			switch mar[0] {
			case "HELLO":
				err := writeText(ctx, c, "HI")
				if err != nil {
					return fmt.Errorf("error replying to HELLO: %w", err)
				}
			case "Y":
				if len(mar) != 2 {
					return protocolError(ctx, c, "Invalid number of arguments for Y")
				}
				_courseID, err := strconv.ParseInt(mar[1], 10, strconv.IntSize)
				if err != nil {
					return protocolError(ctx, c, "Course ID must be an integer")
				}
				courseID := int(_courseID)
				course := func() *courseT {
					coursesLock.RLock()
					defer coursesLock.RUnlock()
					return courses[courseID]
				}()

				err = func() (returnedError error) { /* Named returns so I could modify them in defer */
					tx, err := db.Begin(ctx)
					if err != nil {
						return protocolError(ctx, c, "Database error while beginning transaction")
					}
					defer func() {
						err := tx.Rollback(ctx)
						if err != nil && (!errors.Is(err, pgx.ErrTxClosed)) {
							returnedError = protocolError(ctx, c, "Database error while rolling back transaction in defer block")
							return
						}
					}()

					_, err = tx.Exec(
						ctx, /* TODO: Do we really want this to be in a request context? */
						"INSERT INTO choices (seltime, userid, courseid) VALUES ($1, $2, $3)",
						time.Now().UnixMicro(),
						userID,
						courseID,
					)
					if err != nil {
						var pgErr *pgconn.PgError
						if errors.As(err, &pgErr) && pgErr.Code == "23505" {
							err := writeText(ctx, c, "Y "+mar[1])
							if err != nil {
								return fmt.Errorf("error reaffirming course choice: %w", err)
							}
							return nil
						}
						return protocolError(ctx, c, "Database error while inserting course choice")
					}

					ok := func() bool {
						course.SelectedLock.Lock()
						defer course.SelectedLock.Unlock()
						if course.Selected < course.Max {
							course.Selected++
							go propagateIgnoreFailures(fmt.Sprintf("N %d %d", courseID, course.Selected))
							return true
						}
						return false
					}()

					if ok {
						err := tx.Commit(ctx)
						if err != nil {
							go func() { /* Separate goroutine because we don't need a response from this operation */
								course.SelectedLock.Lock()
								defer course.SelectedLock.Unlock()
								course.Selected--
								propagateIgnoreFailures(fmt.Sprintf("N %d %d", courseID, course.Selected))
							}()
							return protocolError(ctx, c, "Database error while committing transaction")
						}
						err = writeText(ctx, c, "Y "+mar[1])
						if err != nil {
							return fmt.Errorf("error affirming course choice: %w", err)
						}
					} else {
						err := tx.Rollback(ctx)
						if err != nil {
							return protocolError(ctx, c, "Database error while rolling back transaction due to course limit")
						}
						err = writeText(ctx, c, "R "+mar[1]+" :Full")
						if err != nil {
							return fmt.Errorf("error rejecting course choice: %w", err)
						}
					}
					return nil
				}()
				if err != nil {
					return err
				}
			case "N":
				if len(mar) != 2 {
					return protocolError(ctx, c, "Invalid number of arguments for N")
				}
			default:
				return protocolError(ctx, c, "Unknown command "+mar[0])
			}
		}
	}
}
