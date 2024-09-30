/*
 * Course data structures and locking
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

package main

import (
	"context"
	"fmt"
	"sync"
)

type coursetypeT string

type courseT struct {
	ID           int
	Selected     int
	SelectedLock sync.RWMutex
	Max          int
	Title        string
	Type         coursetypeT
	Teacher      string
	Location     string
}

/*
 * const (
 * 	sport      coursetypeT = "Sport"
 * 	enrichment coursetypeT = "Enrichment"
 * 	culture    coursetypeT = "Culture"
 * )
 */

/*
 * The courses are simply stored in a map indexed by the course ID, although
 * the course struct itself also contains an ID field. A lock is embedded
 * inside the struct; we use a lock here instead of a pointer to a lock as
 * it would be easy to forget to initialize the lock when creating the
 * struct. However, this means that the struct could not be copied (though
 * this should only ever happen during creation anyways), therefore we use a
 * pointer to the struct as the value of the map, instead of the struct itself.
 */
var courses map[int](*courseT)

/*
 * This RWMutex is only for massive modifications of the course struct, since
 * locking it on every write would be inefficient; in normal operation the only
 * write that could occur to the courses struct is changing the Selected
 * number, which should be handled with courseT.SelectedLock.
 */
var coursesLock sync.RWMutex

/*
 * Read course information from the database. This should be called during
 * setup. Failure to do so before accessing course information may lead to
 * a null pointer dereference.
 */
func setupCourses() error {
	coursesLock.Lock()
	defer coursesLock.Unlock()

	courses = make(map[int](*courseT))

	rows, err := db.Query(
		context.Background(),
		"SELECT id, nmax, title, ctype, teacher, location FROM courses",
	)
	if err != nil {
		return fmt.Errorf("error fetching courses: %w", err)
	}

	for {
		if !rows.Next() {
			err := rows.Err()
			if err != nil {
				return fmt.Errorf("error fetching courses: %w", err)
			}
			break
		}
		currentCourse := courseT{} //exhaustruct:ignore
		err = rows.Scan(
			&currentCourse.ID,
			&currentCourse.Max,
			&currentCourse.Title,
			&currentCourse.Type,
			&currentCourse.Teacher,
			&currentCourse.Location,
		)
		if err != nil {
			return fmt.Errorf("error fetching courses: %w", err)
		}
		courses[currentCourse.ID] = &currentCourse
	}

	/* TODO: Populate currentCourse.Selected from the database */

	return nil
}
