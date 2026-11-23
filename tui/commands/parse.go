package commands

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/antlr4-go/antlr/v4"
)

var mtxTickersPattern = regexp.MustCompile(`^[A-Za-z,]+$`)

// Parse turns one input line into an uppercase command name and its arguments.
// Argument values are raw token text
func Parse(raw string) (string, map[string]string, error) {
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
	name := strings.ToUpper(commandName(tree))
	if name == "DTX" || name == "FXTX" || name == "MTX" {
		args = reportArgs(name, input)
	}
	if name == "MTX" {
		if months, ok := args["months"]; ok {
			value, parseErr := strconv.Atoi(months)
			if parseErr != nil || value < 0 || value > 99 || strconv.Itoa(value) != months {
				return "", nil, &ParseError{Line: 1, Col: len(input), Msg: "MTX months must be 0 through 99 without leading zeroes", Input: raw}
			}
		}
		if tickers, ok := args["tickers"]; ok && !mtxTickersPattern.MatchString(tickers) {
			return "", nil, &ParseError{Line: 1, Col: len(input), Msg: "MTX tickers must contain only letters and commas", Input: raw}
		}
	}
	return name, args, nil
}

func reportArgs(name, input string) map[string]string {
	fields := strings.Fields(input)
	args := map[string]string{}
	switch name {
	case "DTX":
		var full, brokered, dated map[string]string
		for i := 1; i < len(fields); {
			group := map[string]string{"date": fields[i]}
			i++
			if i < len(fields) && isBrokerToken(fields[i]) {
				group["broker"] = strings.ToUpper(fields[i])
				i++
				if i < len(fields) && !isReportDate(fields[i]) && !isBrokerToken(fields[i]) {
					group["ticker"] = strings.ToUpper(fields[i])
					i++
					full = group
				} else {
					brokered = group
				}
			} else {
				dated = group
			}
		}
		if full != nil {
			return full
		}
		if brokered != nil {
			return brokered
		}
		if dated != nil {
			return dated
		}
	case "FXTX":
		var combined, pairOnly, brokerOnly map[string]string
		for i := 1; i < len(fields); i++ {
			if isBrokerToken(fields[i]) {
				if i+1 < len(fields) && isCurrencyPair(fields[i+1]) {
					combined = map[string]string{"broker": strings.ToUpper(fields[i]), "ccy_pair": strings.ToUpper(fields[i+1])}
					i++
				} else {
					brokerOnly = map[string]string{"broker": strings.ToUpper(fields[i])}
				}
			} else if isCurrencyPair(fields[i]) {
				pairOnly = map[string]string{"ccy_pair": strings.ToUpper(fields[i])}
			}
		}
		if combined != nil {
			return combined
		}
		if pairOnly != nil {
			return pairOnly
		}
		if brokerOnly != nil {
			return brokerOnly
		}
	case "MTX":
		for _, field := range fields[1:] {
			if _, err := strconv.Atoi(field); err == nil {
				args["months"] = field
			} else {
				args["tickers"] = field
			}
		}
	}
	return args
}

func isReportDate(s string) bool { return len(s) == 8 && allDigits(s) }
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func isCurrencyPair(s string) bool {
	return regexp.MustCompile(`^(USD|JPY|CNY|HKD)\.(USD|JPY|CNY|HKD)$`).MatchString(strings.ToUpper(s))
}
func isBrokerToken(s string) bool {
	switch strings.ToUpper(s) {
	case "B1", "B2", "B3", "B4", "B5", "B6", "B7", "IB", "IB2", "IB3", "SWB", "FST", "YJB", "HT":
		return true
	}
	return false
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

// first returns the earliest error with the input attached (one root
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
// No rule-to-command table: rule names diverge from command names (splitCmd->SPLIT, u->U).
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
