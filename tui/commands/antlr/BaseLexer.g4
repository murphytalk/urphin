lexer grammar BaseLexer;

// ──────────────────────────── default mode: command names ────────────────────────────
WS: [ \t\r\n]+ -> skip;

DBUY     : 'DBUY'      -> pushMode(ARGS);
DTX      : 'DTX'       -> pushMode(ARGS);
FXTX     : 'FXTX'      -> pushMode(ARGS);
GOAL     : 'GOAL'      -> pushMode(ARGS);
HELP     : 'HELP'      -> pushMode(ARGS);
MKT      : 'MKT'       -> pushMode(ARGS);
MTX      : 'MTX'       -> pushMode(ARGS);
MNQ      : 'MNQ'       -> pushMode(ARGS);
NEXT0    : 'NEXT0'     -> pushMode(ARGS);
NEXT     : 'NEXT'      -> pushMode(ARGS);
POS      : 'POS'       -> pushMode(ARGS);
RATIO    : 'RATIO'     -> pushMode(ARGS);
REBALANCE: 'REBALANCE' -> pushMode(ARGS);
SPLIT    : 'SPLIT'     -> pushMode(ARGS);
TRANS    : 'TRANS'     -> pushMode(ARGS);
TX       : 'TX'        -> pushMode(ARGS);
ATX      : 'ATX'       -> pushMode(ARGS);
U        : 'U'         -> pushMode(ARGS);
PROP     : 'PROP'      -> pushMode(PROP_MODE);

UNKNOWN_WORD : [A-Z0-9_-]+ ;
UNKNOWN_CHAR : . ;

fragment CCY_FRAG     : 'USD' | 'JPY' | 'CNY' | 'HKD' ;
fragment TICKER_FRAG
  : [0-9][0-9][0-9][0-9] '.HK'
  | [0-9][0-9][0-9][0-9][0-9][0-9] '.SZ'
  | [0-9][0-9][0-9][0-9][0-9][0-9] '.SS'
  | [0-9][0-9][0-9][0-9A-Z] '.T'
  | [A-Z][A-Z][A-Z] '.' [A-Z][A-Z][A-Z]
  | '/'? [A-Z] [A-Z]? [A-Z]? [A-Z]? [A-Z]? ('.' [A-Z] [A-Z]?)? ;

// ──────────────────────────── ARGS mode ────────────────────────────
mode ARGS;

DATE : [0-9][0-9][0-9][0-9] [01][0-9] [0-3][0-9] ;
CCY      : CCY_FRAG ;
CCY_PAIR : CCY_FRAG '.' CCY_FRAG ;
SIDE      : 'BUY' | 'SELL' | 'SPLIT' ;
FILTER    : 'ALL' | 'CLOSE' | 'OPEN' ;
TRUE_KW   : 'TRUE' ;
BOOL_KW   : 'FALSE' | 'YES' | 'NO' ;
BROKER1  : 'B1' ;
BROKER2  : 'B2' ;
BROKER3  : 'B3' ;
BROKER4  : 'B4' ;
BROKER5  : 'B5' ;
BROKER6  : 'B6' ;
BROKER7  : 'B7' ;
TICKER : TICKER_FRAG ;
TICKERS_LIST : [A-Z,]+ ;
INT   : [0-9]+ ;
FLOAT : '-'? [0-9]* '.' [0-9]+ ;
ARGS_WS  : [ \t\r\n]+ -> skip ;
ARGS_ANY : . ;

// PROP mode
mode PROP_MODE;
PROP_TX        : 'TX' ;
PROP_TICKER_KW : 'TICKER' ;
PROP_NONE      : 'NONE' ;
P_BROKER1  : 'B1' -> type(BROKER1);
P_BROKER2  : 'B2' -> type(BROKER2);
P_BROKER3  : 'B3' -> type(BROKER3);
P_BROKER4  : 'B4' -> type(BROKER4);
P_BROKER5  : 'B5' -> type(BROKER5);
P_BROKER6  : 'B6' -> type(BROKER6);
P_BROKER7  : 'B7' -> type(BROKER7);
P_TICKER      : TICKER_FRAG -> type(TICKER);
P_INT         : [0-9]+ -> type(INT);
PROP_PATH  : [A-Z0-9_/]+ ;
PROP_VALUE : ~[ \t\r\n]+ ;
PROP_WS  : [ \t\r\n]+ -> skip ;
PROP_ANY : . ;
