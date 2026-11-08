import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;
import org.antlr.v4.Tool;
import org.antlr.v4.parse.ANTLRParser;
import org.antlr.v4.tool.Grammar;
import org.antlr.v4.tool.Rule;
import org.antlr.v4.tool.ast.GrammarAST;

public final class GenerateCommandUsage {
  private static final Set<String> SCALARS = Set.of("brokers", "forceSpend", "number", "propWord", "tickerArg", "tickerList");
  private final Grammar parser;
  private final Map<String, String> literals;
  private final Map<String, String> usages = new TreeMap<>();

  private GenerateCommandUsage(Grammar parser, Map<String, String> literals) {
    this.parser = parser;
    this.literals = literals;
  }

  public static void main(String[] args) throws Exception {
    if (args.length != 5) throw new IllegalArgumentException("usage: GenerateCommandUsage <grammar-dir> <output-dir> <parser.g4> <lexer.g4> <package>");
    if (!Tool.VERSION.startsWith("4.13.")) throw new IllegalStateException("ANTLR tool must be 4.13.x, found " + Tool.VERSION);
    Path grammarDir = Path.of(args[0]);
    Path outputDir = Path.of(args[1]);
    Grammar parser = load(grammarDir, outputDir, Path.of(args[2]));
    Grammar lexer = load(grammarDir, outputDir, Path.of(args[3]));
    Map<String, String> literals = new LinkedHashMap<>();
    for (Rule rule : lexer.rules.values()) {
      String literal = rule.ast.getChildCount() > 1 ? singleLiteral((GrammarAST) rule.ast.getChild(1)) : null;
      if (literal != null) literals.put(rule.name, literal);
    }
    GenerateCommandUsage generator = new GenerateCommandUsage(parser, literals);
    generator.generate();
    generator.verifyExamples(parser.name);
    generator.write(outputDir.resolve("command_usage.go"), args[4]);
  }

  private static Grammar load(Path lib, Path output, Path file) {
    Tool tool = new Tool();
    tool.libDirectory = lib.toString();
    tool.outputDirectory = output.toString();
    Grammar grammar = tool.loadGrammar(file.toString());
    if (tool.getNumErrors() != 0 || grammar == null) throw new IllegalStateException("ANTLR could not load " + file);
    return grammar;
  }

  private void generate() {
    Rule dispatch = parser.getRule("baseCmd");
    if (dispatch == null) throw fail("baseCmd", "missing dispatch rule");
    GrammarAST block = child(dispatch.ast, 1);
    for (int i = 0; i < block.getChildCount(); i++) {
      GrammarAST alt = child(block, i);
      if (alt.getChildCount() != 1 || child(alt, 0).getType() != ANTLRParser.RULE_REF) throw fail("baseCmd", "expected command rule alternative");
      String ruleName = child(alt, 0).getText();
      Rule rule = parser.getRule(ruleName);
      if (rule == null) throw fail(ruleName, "unresolved command rule");
      String name = commandName(rule);
      String usage = render(child(rule.ast, 1), ruleName, new ArrayList<>());
      usage = usage.trim();
      if (usages.putIfAbsent(name, usage) != null) throw fail(ruleName, "duplicate command " + name);
    }
    if (usages.isEmpty()) throw fail("baseCmd", "no commands found");
  }

  private void verifyExamples(String grammar) {
    if (!grammar.equals("PublicParser")) return;
    Map<String, String> expected = Map.of(
      "TX", "TX [<B> [<T>] | <C> | <T>]",
      "MKT", "MKT <Ccy> <T>",
      "NEXT", "NEXT [<A> [<Fo> [<Fs>]] | <Fs>]",
      "PROP", "PROP (TX <B> <T> <Id> | TICKER <T>) [<Path> (<Val> | NONE)]",
      "MNQ", "MNQ [<D> [<E>]]",
      "WATCH", "WATCH [<Portfolio>]",
      "HELP", "HELP");
    expected.forEach((name, usage) -> {
      if (!usage.equals(usages.get(name))) throw fail(name, "usage changed: expected " + usage + ", got " + usages.get(name));
    });
  }

  private String commandName(Rule rule) {
    GrammarAST body = child(rule.ast, 1);
    GrammarAST first = child(child(body, 0), 0);
    if (first.getType() != ANTLRParser.TOKEN_REF) throw fail(rule.name, "command must begin with a lexer token");
    String token = first.getText();
    String literal = literals.get(token);
    if (literal == null) throw fail(rule.name, "missing literal for command token " + token);
    return literal.toUpperCase();
  }

  private String render(GrammarAST node, String owner, List<String> stack) {
    switch (node.getType()) {
      case ANTLRParser.BLOCK: return renderBlock(node, owner, stack);
      case ANTLRParser.ALT: return renderSequence(node, owner, stack);
      case ANTLRParser.RULE_REF: return renderRule(node.getText(), owner, stack);
      case ANTLRParser.TOKEN_REF: {
        String literal = literals.get(node.getText());
        if (literal == null) throw fail(owner, "missing lexer literal for token " + node.getText());
        return literal;
      }
      case ANTLRParser.EOF: return "";
      case ANTLRParser.OPTIONAL: return "[" + renderGroup(child(node, 0), owner, stack) + "]";
      case ANTLRParser.CLOSURE: return "[" + renderGroup(child(node, 0), owner, stack) + "]...";
      case ANTLRParser.POSITIVE_CLOSURE: return renderRepeated(child(node, 0), owner, stack) + "...";
      case ANTLRParser.ASSIGN: return renderLabel(child(node, 0).getText(), child(node, 1), owner, stack);
      case ANTLRParser.EPSILON: return "";
      default: throw fail(owner, "unsupported grammar node " + node.getText() + " (" + node.getType() + ")");
    }
  }

  private String renderBlock(GrammarAST block, String owner, List<String> stack) {
    List<String> alts = new ArrayList<>();
    for (int i = 0; i < block.getChildCount(); i++) alts.add(render(child(block, i), owner, stack));
    alts.removeIf(String::isBlank);
    if (alts.size() > 1) return "(" + String.join(" | ", alts) + ")";
    return alts.isEmpty() ? "" : alts.get(0);
  }

  private String renderSequence(GrammarAST alt, String owner, List<String> stack) {
    List<String> parts = new ArrayList<>();
    for (int i = 0; i < alt.getChildCount(); i++) {
      String part = render(child(alt, i), owner, stack);
      if (!part.isBlank()) parts.add(part);
    }
    return String.join(" ", parts);
  }

  private String renderRepeated(GrammarAST child, String owner, List<String> stack) {
    String value = render(child, owner, stack);
    return child.getType() == ANTLRParser.BLOCK ? "(" + value + ")" : value;
  }

  private String renderGroup(GrammarAST node, String owner, List<String> stack) {
    if (node.getType() != ANTLRParser.BLOCK) return render(node, owner, stack);
    List<String> alts = new ArrayList<>();
    for (int i = 0; i < node.getChildCount(); i++) alts.add(render(child(node, i), owner, stack));
    alts.removeIf(String::isBlank);
    return String.join(" | ", alts);
  }

  private String renderLabel(String label, GrammarAST value, String owner, List<String> stack) {
    if (value.getType() == ANTLRParser.TOKEN_REF && literals.containsKey(value.getText())) return literals.get(value.getText());
    return "<" + Character.toUpperCase(label.charAt(0)) + label.substring(1) + ">";
  }

  private String renderRule(String name, String owner, List<String> stack) {
    if (SCALARS.contains(name)) return "<" + name + ">";
    if (stack.contains(name)) throw fail(owner, "rule cycle: " + String.join(" -> ", stack) + " -> " + name);
    Rule rule = parser.getRule(name);
    if (rule == null) throw fail(owner, "unresolved rule " + name);
    stack.add(name);
    String result = render(child(rule.ast, 1), owner, stack);
    stack.remove(stack.size() - 1);
    return result;
  }

  private void write(Path output, String pkg) throws IOException {
    Files.createDirectories(output.getParent());
    Path temp = Files.createTempFile(output.getParent(), "command_usage", ".go.tmp");
    StringBuilder go = new StringBuilder("// Code generated by antlr_generate.sh. DO NOT EDIT.\npackage ").append(pkg).append("\n\nvar CommandUsage = map[string]string{\n");
    usages.forEach((name, usage) -> go.append("\t").append(quote(name)).append(": ").append(quote(usage)).append(",\n"));
    go.append("}\n");
    Files.writeString(temp, go.toString());
    try {
      Process p = new ProcessBuilder("gofmt", "-w", temp.toString()).inheritIO().start();
      if (p.waitFor() != 0) throw new IOException("gofmt failed for " + temp);
      Files.move(temp, output, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
    } catch (InterruptedException e) {
      Thread.currentThread().interrupt();
      throw new IOException("interrupted while formatting generated usage", e);
    } finally {
      Files.deleteIfExists(temp);
    }
  }

  private static String quote(String s) { return "\"" + s.replace("\\", "\\\\").replace("\"", "\\\"") + "\""; }
  private static GrammarAST child(GrammarAST node, int i) { if (i >= node.getChildCount()) throw new IllegalStateException("unexpected AST shape at " + node.toStringTree()); return (GrammarAST) node.getChild(i); }
  private static String singleLiteral(GrammarAST node) {
    if (node == null) return null;
    if (node.getType() != ANTLRParser.BLOCK || node.getChildCount() != 1) return null;
    GrammarAST alt = child(node, 0);
    if (alt.getType() == ANTLRParser.LEXER_ALT_ACTION) alt = child(alt, 0);
    if (alt.getType() != ANTLRParser.ALT || alt.getChildCount() != 1) return null;
    GrammarAST value = child(alt, 0);
    if (value.getType() != ANTLRParser.STRING_LITERAL) return null;
    String text = value.getText();
    return text.substring(1, text.length() - 1);
  }
  private static IllegalStateException fail(String rule, String message) { return new IllegalStateException("command usage " + rule + ": " + message); }
}
