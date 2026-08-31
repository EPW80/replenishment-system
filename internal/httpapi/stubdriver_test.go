package httpapi

import (
	"database/sql"
	"database/sql/driver"
)

// stubDriver is a minimal database/sql driver whose only job is to make
// (*sql.DB).PingContext succeed, so the health handler's own logic can be tested
// without a live database. The store package's integration tests cover real
// connections.
type stubDriver struct{}

func (stubDriver) Open(string) (driver.Conn, error) { return stubConn{}, nil }

type stubConn struct{}

func (stubConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (stubConn) Close() error                        { return nil }
func (stubConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func init() { sql.Register("stubdriver", stubDriver{}) }
