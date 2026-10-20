package commands

import (
	"fmt"
	"strings"

	"github.com/antlr4-go/antlr/v4"
)

// Parse turns one input line into an uppercase command name and its arguments.
// Argument values are raw token text, except broker, which is always the
// canonical code B1..B7 regardless of the build.
func Parse(raw string) (string, map[string]any, error) {
	input := strings.TrimSpace(raw)
	if input == "" {
		return "", nil, &ParseError{Line: 1, Col: 0, Msg: "empty command", Input: raw}
	}

	errs := &errorCollector{}
	tree, toks := buildTree(input, errs)
	if len(errs.errs) > 0 {
		return "", nil, errs.first(raw)
	}

	args, err := extract(tree, toks)
	if err != nil {
		return "", nil, err
	}
	// Only the name is uppercased: case folding is the lexer's job and arg
	// values must keep the user's casing (F9).
	return strings.ToUpper(commandName(tree)), args, nil
}

// errorCollector implements antlr.ErrorListener and records errors as ParseError
// instead of printing them, so stderr stays clean in the TUI.
type errorCollector struct {
	*antlr.DefaultErrorListener
	errs []*ParseError
}

func (c *errorCollector) SyntaxError(_ antlr.Recognizer, _ any, line, col int,
	msg string, _ antlr.RecognitionException) {
	c.errs = append(c.errs, &ParseError{Line: line, Col: col, Msg: msg})
}

// first returns the earliest error with the input attached (F15: one root
// cause can produce several callbacks, so only the first is reported).
func (c *errorCollector) first(input string) error {
	e := *c.errs[0]
	e.Input = input
	if len(c.errs) > 1 {
		e.Msg = fmt.Sprintf("%s (+%d more)", e.Msg, len(c.errs)-1)
	}
	return &e
}

// commandName walks the leftmost chain prog -> cmd -> baseCmd -> <rule> -> keyword
// (5 levels since F14; bound 8 tolerates one more inserted layer). No
// rule-to-command table: rule names diverge from command names (splitCmd->SPLIT, u->U).
func commandName(tree antlr.ParserRuleContext) string {
	var n antlr.Tree = tree
	for depth := 0; depth < 8; depth++ {
		if t, ok := n.(antlr.TerminalNode); ok {
			return t.GetText()
		}
		pc, ok := n.(antlr.ParserRuleContext)
		if !ok || pc.GetChildCount() == 0 {
			return ""
		}
		n = pc.GetChild(0)
	}
	return ""
}
