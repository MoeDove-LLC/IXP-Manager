package api

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

// ReadObject reads exactly one existing activation target. It accepts no
// caller-supplied path, property list, filter expression or display index.
// The returned dictionary is an actual row, not a whole-device snapshot.
func (c *Client) ReadObject(ctx context.Context, resource Resource, id string) (Record, error) {
	if resource != FilterRule && resource != BGPInstance && resource != BGPConnection || !objectID.MatchString(id) {
		return nil, &Error{Code: CodeConfig}
	}
	result, err := c.readObjectPresence(ctx, resource, id, resource == FilterRule)
	if err != nil {
		return nil, err
	}
	if !result.Present {
		return nil, &Error{Code: CodeProtocol, Resource: resource}
	}
	return result.Record, nil
}

// ObjectPresence distinguishes a completed zero-row query from an unknown
// outcome. Only a nil error makes Present meaningful. It is one actual object
// read, never a whole-device snapshot or proof of deletion authorization.
type ObjectPresence struct {
	Present bool
	Record  Record
}

// ReadObjectPresence uses a fixed menu and exact native ID. Missing objects are
// accepted only after a fully tagged, successful !done with no unexpected
// attributes. Traps, EOF, deadlines and malformed replies remain errors.
func (c *Client) ReadObjectPresence(ctx context.Context, resource Resource, id string) (ObjectPresence, error) {
	if resource != RoutingTable && resource != FilterRule && resource != BGPInstance && resource != BGPConnection || !objectID.MatchString(id) {
		return ObjectPresence{}, &Error{Code: CodeConfig}
	}
	return c.readObjectPresence(ctx, resource, id, false)
}

func (c *Client) readObjectPresence(ctx context.Context, resource Resource, id string, existingRule bool) (ObjectPresence, error) {
	definition := dictionaries[resource]
	selector := "?.id=" + id
	if existingRule {
		// Native 7.24.6 verified: direct selection avoids a full filter-rule
		// query scan. Missing IDs produce a trap, not absence evidence; use
		// this only where one existing rule is required. Presence/absence
		// queries retain their completed-zero-row protocol below.
		selector = "=from=" + id
	}
	reply, err := c.command(ctx, []string{definition.path + "/print", "=.proplist=" + strings.Join(definition.properties, ","), selector}, false, false)
	if err != nil {
		var failure *Error
		if errors.As(err, &failure) {
			copy := *failure
			copy.Resource = resource
			return ObjectPresence{}, &copy
		}
		return ObjectPresence{}, &Error{Code: CodeTransport, Resource: resource}
	}
	if len(reply.rows) > 1 || len(reply.done) != 0 {
		return ObjectPresence{}, &Error{Code: CodeProtocol, Resource: resource}
	}
	if len(reply.rows) == 0 {
		return ObjectPresence{Present: false}, nil
	}
	raw := reply.rows[0]
	if !objectID.MatchString(raw[".id"]) || !strings.EqualFold(raw[".id"], id) {
		return ObjectPresence{}, &Error{Code: CodeProtocol, Resource: resource}
	}
	row := Record{}
	for _, name := range definition.properties {
		if value, present := raw[name]; present {
			if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
				return ObjectPresence{}, &Error{Code: CodeProtocol, Resource: resource}
			}
			row[name] = value
		}
	}
	for _, name := range definition.required {
		if row[name] == "" {
			return ObjectPresence{}, &Error{Code: CodeProtocol, Resource: resource}
		}
	}
	return ObjectPresence{Present: true, Record: row}, nil
}

// ReadIdentity retrieves actual device/version evidence only. Routing menu
// capabilities are deliberately absent so this cannot stand in for Inventory.
func (c *Client) ReadIdentity(ctx context.Context) (Inventory, error) {
	result := Inventory{Capabilities: map[Resource]Capability{}}
	for _, entry := range []struct {
		resource Resource
		rows     *[]Record
		optional bool
	}{{SystemResource, &result.Resources, false}, {SystemLicense, &result.Licenses, true}, {RouterBoard, &result.RouterBoards, true}} {
		rows, err := c.Read(ctx, entry.resource)
		if err != nil {
			var failure *Error
			if entry.optional && errors.As(err, &failure) && failure.Code == CodeUnsupported {
				result.Capabilities[entry.resource] = Unsupported
				continue
			}
			return Inventory{}, err
		}
		if len(rows) > 1 || !entry.optional && len(rows) != 1 {
			return Inventory{}, &Error{Code: CodeProtocol, Resource: entry.resource}
		}
		*entry.rows = rows
		result.Capabilities[entry.resource] = Supported
	}
	return result, nil
}
