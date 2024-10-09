CREATE TABLE courses (
	id INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	nmax INTEGER NOT NULL,
	title TEXT NOT NULL,
	teacher TEXT NOT NULL,
	location TEXT NOT NULL,
	ctype TEXT NOT NULL,
	cgroup TEXT NOT NULL
);
CREATE TABLE users (
	id TEXT PRIMARY KEY NOT NULL, -- should be UUID
	name TEXT NOT NULL,
	email TEXT NOT NULL,
	department TEXT NOT NULL,
	session TEXT,
	expr BIGINT -- seconds
);
CREATE TABLE choices (
	PRIMARY KEY (userid, courseid),
	seltime BIGINT NOT NULL, -- microseconds
	userid TEXT NOT NULL, -- should be UUID
	FOREIGN KEY(userid) REFERENCES users(id),
	courseid INTEGER NOT NULL,
	FOREIGN KEY(courseid) REFERENCES courses(id),
	UNIQUE (userid, courseid)
);
CREATE TABLE misc (
	key TEXT PRIMARY KEY NOT NULL,
	value INTEGER NOT NULL
);
