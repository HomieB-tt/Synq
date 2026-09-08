package db

import (
	_ "modernc.org/sqlite"
)

// driverName is the database/sql driver name registered by
// modernc.org/sqlite, a CGO-free, pure-Go SQLite implementation. This
// keeps Synq's build free of a C toolchain requirement on any platform.
const driverName = "sqlite"
