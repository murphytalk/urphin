//go:build extension

package commands

import (
	"github.com/antlr4-go/antlr/v4"
	extparser "github.com/murphytalk/urphin/extension/tui/commands/antlr/generated"
)

func buildTree(input string, errs *errorCollector) (antlr.ParserRuleContext, antlr.TokenStream) {
	lexer := extparser.NewUrphinCommandLexer(antlr.NewInputStream(input))
	lexer.RemoveErrorListeners() // default listeners print to stderr, polluting the TUI
	lexer.AddErrorListener(errs)
	stream := antlr.NewCommonTokenStream(lexer, antlr.TokenDefaultChannel)
	p := extparser.NewUrphinCommandParser(stream)
	p.RemoveErrorListeners()
	p.AddErrorListener(errs)
	return p.Prog(), stream
}
