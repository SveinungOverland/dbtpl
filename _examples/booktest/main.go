// Command booktest is an example of using a similar schema on different
// databases.
//
//go:debug x509negativeserial=1
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/user"

	// drivers
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"

	// models
	"github.com/SveinungOverland/dbtpl/_examples/booktest/mysql"
	"github.com/SveinungOverland/dbtpl/_examples/booktest/oracle"
	"github.com/SveinungOverland/dbtpl/_examples/booktest/postgres"
	"github.com/SveinungOverland/dbtpl/_examples/booktest/sqlite3"
	"github.com/SveinungOverland/dbtpl/_examples/booktest/sqlserver"

	"github.com/xo/dburl"
	"github.com/xo/dburl/passfile"
)

func main() {
	verbose := flag.Bool("v", false, "verbose")
	dsn := flag.String("dsn", "", "dsn")
	flag.Parse()
	if err := run(context.Background(), *verbose, *dsn); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, verbose bool, dsn string) error {
	if verbose {
		logger := func(s string, v ...any) {
			fmt.Printf("-------------------------------------\nQUERY: %s\n  VAL: %v\n\n", s, v)
		}
		mysql.SetLogger(logger)
		oracle.SetLogger(logger)
		postgres.SetLogger(logger)
		sqlite3.SetLogger(logger)
		sqlserver.SetLogger(logger)
	}
	v, err := user.Current()
	if err != nil {
		return err
	}
	// parse url
	u, err := parse(dsn)
	if err != nil {
		return err
	}
	// open database
	db, err := passfile.OpenURL(u, v.HomeDir, "dbtplpass")
	if err != nil {
		return err
	}
	var f func(context.Context, *sql.DB) error
	switch u.Driver {
	case "mysql":
		f = runMysql
	case "oracle":
		f = runOracle
	case "postgres":
		f = runPostgres
	case "sqlite3":
		f = runSqlite3
	case "sqlserver":
		f = runSqlserver
	}
	return f(ctx, db)
}

func parse(dsn string) (*dburl.URL, error) {
	v, err := dburl.Parse(dsn)
	if err != nil {
		return nil, err
	}
	switch v.Driver {
	case "mysql":
		q := v.Query()
		q.Set("parseTime", "true")
		v.RawQuery = q.Encode()
		return dburl.Parse(v.String())
	case "sqlite3":
		q := v.Query()
		q.Set("_loc", "auto")
		v.RawQuery = q.Encode()
		return dburl.Parse(v.String())
	}
	return v, nil
}
