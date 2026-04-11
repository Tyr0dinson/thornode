#!/usr/bin/python3

import json
import sys

if len(sys.argv) != 4:
    print(f"usage: {sys.argv[0]} <specfile> <account> <amount>")
    sys.exit(1)

specfile, account, amount  = sys.argv[1:]

with open(specfile) as f:
    data = json.load(f)

balances = data['genesis']['runtimeGenesis']['patch']['balances']['balances']
balances.append([account, int(amount)])

with open(specfile, 'w') as f:
    json.dump(data, f, indent=2)
