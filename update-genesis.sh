BYTECODE=$(cat mailbox1 | tr -d '\n')
sed -i "s/\"alloc\": {/\"alloc\": {\n    \"aCfc748C73630F3a6E4fEEc8cF7695Eeb4836088\": {\n      \"balance\": \"0x0\",\n      \"code\": \"$BYTECODE\"\n    },/" $WORKDIR/genesis.json
 
BYTECODE=$(grep "^0x" pingpong1)
sed -i "s/\"alloc\": {/\"alloc\": {\n    \"C870117aC68BCc0D85A0C9ED1B06b29f5E834011\": {\n      \"balance\": \"0x0\",\n   \"nonce\": \"0x1\",\n  \"storage\": { \"0x000000000000000000000000000000000000000    0000000000000000000000000\": \"0x000000000000000000000000aCfc748C73630F3a6E4fEEc8cF7695Eeb4836088\" },\n \"code\": \"$BYTECODE\"\n    },/" $WORKDIR/genesis.json

