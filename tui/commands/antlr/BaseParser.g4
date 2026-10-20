parser grammar BaseParser;

prog: cmd EOF;

cmd     : baseCmd ;
baseCmd
  : mkt | dtx | tx | fxtx | atx | trans | pos | mtx | mnq
  | goal | splitCmd | next | next0 | rebalance | dbuy
  | ratioCmd
  | help | u
  | prop
  ;

// shared argument shapes (named groups in commands.py)
tickerArg : TICKER | CCY_PAIR | CCY | SIDE | FILTER
          | TRUE_KW | BOOL_KW
          | brokers
          ;
brokers   : BROKER1 | BROKER2 | BROKER3 | BROKER4 | BROKER5 | BROKER6 | BROKER7 ;
number    : INT | FLOAT ;
tickerList: TICKERS_LIST | tickerArg ;

// STOCK
mkt       : MKT ccy=CCY t=tickerArg ;
dtx       : DTX d=DATE (b=brokers (t=tickerArg)?)? ;
tx        : TX (b=brokers (t=tickerArg)? | c=CCY | t=tickerArg)? ;
atx       : ATX b=brokers s=SIDE d=DATE t=tickerArg price=number sh=number fee=number ;
trans     : TRANS t=tickerArg d=DATE b1=brokers b2=brokers (sh=number)? ;
pos       : POS (b=brokers (t=tickerArg)? | t=tickerArg)? ;
mtx       : MTX (m=INT (ts=tickerList)? | ts=tickerList)? ;
goal      : GOAL (a=number)? ;
splitCmd  : SPLIT t=tickerArg ratio=number ;
next      : NEXT (a=number (fo=number (fs=forceSpend)?)? | fs=forceSpend)? ;
next0     : NEXT0 (a=number)? ;
rebalance : REBALANCE (pct=number (fo=number)?)? ;
dbuy      : DBUY ;

// FX
fxtx      : FXTX (b=brokers (ccyPair=CCY_PAIR)? | ccyPair=CCY_PAIR)? ;

// METAL
ratioCmd  : RATIO ;

// urphin
help      : HELP ;
u         : U ;

// PROP
prop        : PROP (txTarget | tickerTarget)
              (path=propWord (val=propWord | none=PROP_NONE))? ;
txTarget    : PROP_TX b=brokers t=tickerArg id=INT ;
tickerTarget: PROP_TICKER_KW t=tickerArg ;
propWord    : PROP_PATH | PROP_VALUE | TICKER | INT
            | brokers
            | PROP_TX | PROP_TICKER_KW ;

forceSpend  : TRUE_KW | BOOL_KW | INT ;

mnq       : MNQ dateRange? ;
dateRange : d=DATE (e=DATE)? ;
