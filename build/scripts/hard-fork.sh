set -ex

HARDFORK_BLOCK_HEIGHT="${HARDFORK_BLOCK_HEIGHT:--1}"
DATE=$(date +%s)

# backup first
cp -r ~/.thornode/config ~/.thornode/config.$DATE.bak
cp -r ~/.thornode/data ~/.thornode/data.$DATE.bak

# export genesis file
thornode export --height $HARDFORK_BLOCK_HEIGHT >thorchain_genesis_export.$DATE.json

# reset the database
thornode unsafe-reset-all

## keep all the settings
#cp ~/.thornode/config.$DATE.bak/addrbook.json ~/.thornode/config/addrbook.json
#cp ~/.thornode/config.$DATE.bak/app.toml ~/.thornode/config/app.toml
#cp ~/.thornode/config.$DATE.bak/config.toml ~/.thornode/config/config.toml
#cp ~/.thornode/config.$DATE.bak/node_key.json ~/.thornode/config/node_key.json
#cp ~/.thornode/config.$DATE.bak/priv_validator_key.json ~/.thornode/config/priv_validator_key.json

# copied exported genesis file to the config directory
cp thorchain_genesis_export.$DATE.json ~/.thornode/config/genesis.json
