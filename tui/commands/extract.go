package commands

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/antlr4-go/antlr/v4"
)

// labelKeys maps each grammar label (getter name minus "Get") to the canonical
// args key of ANTLR_INTEGRATION.md §8. A label missing here fails every parse
// that contains it, so the table must cover all labels in both grammars.
var labelKeys = map[string]string{
	"A": "amount", "B": "broker", "B1": "broker", "B2": "broker2",
	"C": "ccy", "Ccy": "ccy", "CcyPair": "ccy_pair", "Cp": "cp",
	"D": "date", "E": "edate", "E1": "exp_date1", "E2": "exp_date2",
	"F": "filter", "Fee": "fee", "Fo": "fixed_over_spend_amount",
	"Fs": "force_spend", "Id": "tx_id", "IsClosed": "is_closed", "M": "months", "None": "value",
	"O1": "open_date1", "O2": "open_date2", "Path": "prop_path",
	"Pct": "pct", "Prem": "price", "Price": "price", "Ratio": "split_ratio",
	"S": "side", "Sh": "shares", "Strike": "sprice", "T": "ticker",
	"Ts": "tickers", "Val": "value",
}

// frameworkGetters blacklists only Start/Stop: they are the sole
// BaseParserRuleContext getters returning antlr.Token. GetParent and
// GetRuleContext return antlr.RuleContext, which the type filter below excludes.
var frameworkGetters = map[string]bool{"Start": true, "Stop": true}

var (
	tokenType    = reflect.TypeOf((*antlr.Token)(nil)).Elem()
	terminalType = reflect.TypeOf((*antlr.TerminalNode)(nil)).Elem()
	ruleCtxType  = reflect.TypeOf((*antlr.ParserRuleContext)(nil)).Elem()
)

func extract(tree antlr.ParserRuleContext, _ antlr.TokenStream) (map[string]any, error) {
	args := map[string]any{}
	if err := walk(tree, args); err != nil {
		return nil, err
	}
	return args, nil
}

// walk harvests labelled values via reflection: generated label fields are
// unexported, only the Get* getters are visible (F10).
func walk(ctx antlr.ParserRuleContext, args map[string]any) error {
	// labels mark a value the caller wants verbatim; inner labels are implementation detail
	labeled := map[antlr.ParserRuleContext]bool{}
	v := reflect.ValueOf(ctx) // dynamic type, so label getters of the concrete context are enumerable
	for i := 0; i < v.NumMethod(); i++ {
		m := v.Method(i)
		name := v.Type().Method(i).Name
		// v.Method(i) is bound to the receiver: NumIn() excludes it
		if !strings.HasPrefix(name, "Get") || m.Type().NumIn() != 0 || m.Type().NumOut() != 1 {
			continue
		}
		suffix := strings.TrimPrefix(name, "Get")
		if frameworkGetters[suffix] {
			continue
		}
		out := m.Type().Out(0)
		// filter by type before IsNil, otherwise GetChildCount() int and friends panic
		if out != tokenType && !out.Implements(terminalType) && !out.Implements(ruleCtxType) {
			continue
		}
		res := m.Call(nil)[0]
		if res.IsNil() {
			continue
		}
		switch {
		case out == tokenType:
			key, err := keyFor(suffix)
			if err != nil {
				return err
			}
			tk := res.Interface().(antlr.Token).GetText()
			args[key] = normalize(key, tk)
		case out.Implements(terminalType):
			key, err := keyFor(suffix)
			if err != nil {
				return err
			}
			tk := res.Interface().(antlr.TerminalNode).GetSymbol().GetText()
			args[key] = normalize(key, tk)
		case out.Implements(ruleCtxType):
			rc := res.Interface().(antlr.ParserRuleContext)
			key, err := keyFor(suffix)
			if err != nil {
				return err
			}
			args[key] = normalize(key, rc.GetText())
			labeled[rc] = true
		}
	}
	// unlabeled child rules must still be traversed, e.g. txTarget's b/t/id;
	// children already harvested through a labeled getter are not descended into.
	for i := 0; i < ctx.GetChildCount(); i++ {
		if child, ok := ctx.GetChild(i).(antlr.ParserRuleContext); ok && !labeled[child] {
			if err := walk(child, args); err != nil {
				return err
			}
		}
	}
	return nil
}

func keyFor(suffix string) (string, error) {
	if k, ok := labelKeys[suffix]; ok {
		return k, nil
	}
	return "", fmt.Errorf("unmapped grammar label %q: add it to labelKeys", suffix)
}

// caseSensitiveKeys are looked up verbatim: prop_path is a case-sensitive JSON
// path, value is free-form user text (PROP).
var caseSensitiveKeys = map[string]bool{"prop_path": true, "value": true}

// normalize uppercases every other value once here: commands match values against
// the database or uppercase keyword sets, and caseInsensitive preserves user case
// (F9). Denylist so new labels normalize by default (the F14 lesson).
func normalize(key, text string) string {
	if caseSensitiveKeys[key] {
		return text
	}
	return strings.ToUpper(text)
}
