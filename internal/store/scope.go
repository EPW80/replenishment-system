package store

import "fmt"

// Scope limits a query to the rows one caller is allowed to see.
//
// It exists so cross-customer access is impossible at the query layer rather than
// dependent on every handler remembering to compare two customer IDs. A check that has
// to be repeated is a check that will eventually be forgotten in one place, and that
// one place is the whole vulnerability.
//
// The zero value is the system scope, which is unrestricted — that is deliberate for
// the nightly materializer, which operates across every active schedule. What is *not*
// deliberate is an accidental unrestricted read, so callers name their scope
// explicitly: methods taking a Scope take it as a required argument, which forces each
// call site to say which one it means.
type Scope struct {
	// restricted separates "no customer filter" from "a filter for the empty
	// customer". Without it, CustomerScope("") would widen to every row — a bug that
	// reads as a missing value but behaves as a privilege escalation. Restricted with
	// an empty ID matches nothing instead, so the failure mode is denial.
	restricted bool
	customerID string
}

// SystemScope returns an unrestricted scope, for background work that legitimately
// spans customers.
func SystemScope() Scope { return Scope{} }

// CustomerScope returns a scope limited to one customer's rows.
func CustomerScope(customerID string) Scope {
	return Scope{restricted: true, customerID: customerID}
}

// filterOwn returns a predicate for a table that carries customer_id itself, plus the
// arguments it consumes. nextArg is the placeholder number to start from.
func (s Scope) filterOwn(nextArg int) (string, []any) {
	if !s.restricted {
		return "", nil
	}
	return fmt.Sprintf(" AND customer_id = $%d", nextArg), []any{s.customerID}
}

// filterVia returns a predicate for a table that reaches customer_id through a
// schedule reference, such as occurrences.schedule_id.
func (s Scope) filterVia(column string, nextArg int) (string, []any) {
	if !s.restricted {
		return "", nil
	}
	clause := fmt.Sprintf(
		" AND %s IN (SELECT id FROM schedules WHERE customer_id = $%d)", column, nextArg)
	return clause, []any{s.customerID}
}
