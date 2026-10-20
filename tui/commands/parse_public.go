//go:build !extension

package commands

import (
	"github.com/antlr4-go/antlr/v4"
	pubparser "github.com/murphytalk/urphin/tui/commands/antlr/generated"
)

func buildTree(input string, errs *errorCollector) (antlr.ParserRuleContext, antlr.TokenStream) {
	lexer := pubparser.NewPublicLexer(antlr.NewInputStream(input))
	lexer.RemoveErrorListeners() // default listeners print to stderr, polluting the TUI
	lexer.AddErrorListener(errs)
	stream := antlr.NewCommonTokenStream(lexer, antlr.TokenDefaultChannel)
	p := pubparser.NewPublicParser(stream)
	p.RemoveErrorListeners()
	p.AddErrorListener(errs)
	return p.Prog(), stream
}
