// Package dbmode wires the live-database mode into the UI: the connection
// picker and form, schema browsing, table loading and the Redis key browser,
// all through the shared overlay system. Connections are created and opened
// through transport.Client (AGENTS.md 铁律 2).
package dbmode

import (
	"fmt"
	"strings"
	"sync"

	"github.com/polydb/polydb/internal/tui"
	_ "github.com/polydb/polydb/internal/tui/popup" // register the base overlay factories
)

var (
	registerOnce sync.Once

	// Pristine factories captured before wrapping, so registration can
	// never nest wrappers.
	origQuery  func(tui.AppContext, string) (tui.Overlay, error)
	origSchema func(tui.AppContext, string) (tui.Overlay, error)
)

// RegisterFactories installs the database-mode overlay factories:
//
//   - "connect": the saved-connection picker (list / new / edit / delete),
//   - "newconn" / "editconn": the connection form (create / edit),
//   - "opentable": load one live table into a new tab (ns "\t" table arg,
//     matching the schema browser's dispatch encoding),
//   - "query" and "schema" are wrapped so that, with a live key-value
//     connection, they open the raw-command prompt and the key browser
//     instead; without one they delegate to the originals.
func RegisterFactories() {
	registerOnce.Do(func() {
		origQuery = tui.Factories["query"]
		origSchema = tui.Factories["schema"]

		tui.Factories["opentable"] = openTableFactory
	})

	tui.Factories["connect"] = func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
		return newConnPicker(ctx), nil
	}
	tui.Factories["newconn"] = func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
		kind := strings.TrimSpace(arg)
		if _, ok := engineSpecs[kind]; !ok {
			return nil, fmt.Errorf("newconn: unknown database kind: %s", kind)
		}
		return newConnForm(ctx, kind), nil
	}
	tui.Factories["editconn"] = func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
		info, err := ctx.Client().GetConnection(strings.TrimSpace(arg))
		if err != nil {
			return nil, err
		}
		return newEditForm(ctx, info), nil
	}

	tui.Factories["query"] = func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
		if ctx.KV() != nil {
			return newRedisPrompt(ctx, arg), nil
		}
		if origQuery != nil {
			return origQuery(ctx, arg)
		}
		return nil, fmt.Errorf("query: no connection")
	}
	tui.Factories["schema"] = func(ctx tui.AppContext, arg string) (tui.Overlay, error) {
		if kv := ctx.KV(); kv != nil {
			return newKeyBrowser(kv, len(ctx.Tabs())), nil
		}
		if origSchema != nil {
			return origSchema(ctx, arg)
		}
		return nil, fmt.Errorf("schema: no connection")
	}
}

// splitTableArg decodes the "ns\ttable" argument produced by the schema
// browser; a plain table name (no tab) is accepted with an empty namespace.
func splitTableArg(arg string) (ns, table string) {
	if i := strings.IndexByte(arg, '\t'); i >= 0 {
		return arg[:i], arg[i+1:]
	}
	return "", arg
}
