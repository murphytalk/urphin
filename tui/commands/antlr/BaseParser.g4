parser grammar BaseParser;

prog: cmd EOF;

cmd     : baseCmd ;
baseCmd
  : dtx | tx | fxtx | atx | trans | pos | mtx
  | splitCmd
  | help
  | prop
  | watch
  ;

// shared argument shapes (named groups in commands.py)
tickerArg : TICKER | PORTFOLIO | CCY_PAIR | CCY | SIDE | FILTER
          | TRUE_KW | BOOL_KW
          | brokers
          ;
brokers   : BROKER1 | BROKER2 | BROKER3 | BROKER4 | BROKER5 | BROKER6 | BROKER7 ;
number    : INT | FLOAT ;
tradeNumber : TRADE_NUMBER | DATE | number ;
tickerList: TICKERS_LIST | tickerArg ;

// STOCK
dtx       : DTX (d1=DATE b1=brokers t1=tickerArg | d2=DATE b2=brokers | d3=DATE)+ ;
tx        : TX (b=brokers (t=tickerArg)? | c=CCY | t=tickerArg)? ;
atx       : ATX b=brokers s=SIDE d=DATE t=tickerArg price=tradeNumber sh=tradeNumber fee=tradeNumber ;
trans     : TRANS t=tickerArg d=DATE b1=brokers b2=brokers (sh=tradeNumber)? ;
pos       : POS (b=brokers (t=tickerArg)? | t=tickerArg)? ;
mtx       : MTX (m1=mtxMonths ts1=mtxTickerList | ts2=mtxTickerList | m2=mtxMonths)* ;
mtxMonths : INT ;
mtxTickerList : TICKERS_LIST | TICKER | PORTFOLIO ;
splitCmd  : SPLIT t=tickerArg ratio=number ;

// FX
fxtx      : FXTX (b1=brokers cp1=CCY_PAIR | cp2=CCY_PAIR | b2=brokers)+ ;

// METAL

// urphin
help      : HELP ;

// PROP
prop        : PROP (txTarget | tickerTarget)
              (path=propWord (val=propWord | none=PROP_NONE))? ;
txTarget    : PROP_TX b=brokers t=tickerArg id=INT ;
tickerTarget: PROP_TICKER_KW t=tickerArg ;
propWord    : PROP_PATH | PROP_VALUE | TICKER | INT
            | PORTFOLIO
            | brokers
            | PROP_TX | PROP_TICKER_KW ;


forceSpend  : TRUE_KW | BOOL_KW | INT ;

watch     : WATCH (portfolio=PORTFOLIO)? ;
dateRange : d=DATE (e=DATE)? ;
