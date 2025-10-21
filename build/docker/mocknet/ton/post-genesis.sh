#!/bin/bash

echo "started post-genesis.sh"
cp /usr/local/bin/fift /usr/bin/
cd /usr/share/ton/smartcont

NODE_KEY="E7XwFSQzNkcRepUC23J2nRpASXpnsEKmyyHYV4u/FZY="

while true; do
    sleep 10
    # Faucet wallet (basechain)
    echo top up 0:8a12303dd951ae92997827cb5fac56dc068ae6bed705f16ebde8b7877a429866
    chmod +x wallet.fif
    ./wallet.fif main-wallet 0QCKEjA92VGukpl4J8tfrFbcBormvtcF8W696LeHekKYZlRD 0 1000000
    /usr/local/bin/lite-client -a 127.0.0.1:40004 -b "$NODE_KEY" -t 3 -c "sendfile /usr/share/ton/smartcont/wallet-query.boc"
    sleep 15

    /usr/local/bin/lite-client -a 127.0.0.1:40004 -b "$NODE_KEY" -t 3 -c "getaccount 0QCKEjA92VGukpl4J8tfrFbcBormvtcF8W696LeHekKYZlRD" | grep "account balance" && break
done

echo "finished post-genesis.sh"
