// SPDX-License-Identifier: MIT
// -------------------
// Router Version: 5.0
// -------------------
pragma solidity 0.8.20;

// ERC20 Interface
interface iERC20 {
    function balanceOf(address) external view returns (uint256);
    function burn(uint) external;
}
// RUNE Interface
interface iRUNE {
    function transferTo(address, uint) external returns (bool);
}
// ROUTER Interface
interface iROUTER {
    function depositWithExpiry(address, address, uint, string calldata, uint) external;
}

struct TransferDetails {
    address payable to;    // Address of the recipient
    address asset;         // Address of the asset (token) to transfer
    uint256 amount;        // Amount of the asset to transfer
    string memo;           // Memo for the transfer
}

struct TransferAndCallDetails {
    address payable target;       // Target contract address for the call
    address fromAsset;            // Address of the asset (token) being transferred from
    uint256 fromAmount;           // Amount of the asset to transfer from
    address toAsset;              // Address of the asset (token) being transferred to
    address recipient;            // Final recipient address
    uint256 amountOutMin;         // Minimum amount out in the swap
    string memo;                  // Memo for the operation
    uint256[] flags;              // Additional flags or parameters
}

// THORChain_Router is managed by THORChain Vaults
contract THORChain_Router {
    address public RUNE;

    struct Coin {
        address asset;
        uint amount;
    }

    // Vault allowance for each asset
    mapping(address => mapping(address => uint)) private _vaultAllowance;

    uint256 private constant _NOT_ENTERED = 1;
    uint256 private constant _ENTERED = 2;
    uint256 private _status;

    // Emitted for all deposits, the memo distinguishes for swap, add, remove, donate etc
    event Deposit(address indexed to, address indexed asset, uint amount, string memo);

    // Emitted for all outgoing transfers, the vault dictates who sent it, memo used to track.
    event TransferOut(address indexed vault, address indexed to, address asset, uint amount, string memo);

    // Emitted for all outgoing transferAndCalls, the vault dictates who sent it, memo used to track.
    event TransferOutAndCall(address indexed vault, address target, uint amount, address finalAsset, address indexed to, uint256 amountOutMin, string memo, uint256[] flags);

    // Changes the spend allowance between vaults
    event TransferAllowance(address indexed oldVault, address indexed newVault, address asset, uint amount, string memo);

    // Specifically used to batch send the entire vault assets
    event VaultTransfer(address indexed oldVault, address indexed newVault, Coin[] coins, string memo);

    modifier nonReentrant() {
        require(_status != _ENTERED, "ReentrancyGuard: reentrant call");
        _status = _ENTERED;
        _;
        _status = _NOT_ENTERED;
    }

    constructor(address rune) {
        RUNE = rune;
        _status = _NOT_ENTERED;
    }

    // Deposit with Expiry (preferred)
    function depositWithExpiry(address payable vault, address asset, uint amount, string memory memo, uint expiration) external payable {
        require(block.timestamp < expiration, "THORChain_Router: expired");
        deposit(vault, asset, amount, memo);
    }

    // Deposit an asset with a memo. ETH is forwarded, ERC-20 stays in ROUTER
    function deposit(address payable vault, address asset, uint amount, string memory memo) private nonReentrant{
        uint safeAmount;
        if(asset == address(0)){
            safeAmount = msg.value;
            bool success = vault.send(safeAmount);
            require(success);
        } else {
            require(msg.value == 0, "unexpected eth");  // protect user from accidentally locking up eth
            if(asset == RUNE) {
                safeAmount = amount;
                iRUNE(RUNE).transferTo(address(this), amount);
                iERC20(RUNE).burn(amount);
            } else {
                safeAmount = safeTransferFrom(asset, amount); // Transfer asset
                _vaultAllowance[vault][asset] += safeAmount; // Credit to chosen vault
            }
        }
        emit Deposit(vault, asset, safeAmount, memo);
    }

    //############################## ALLOWANCE TRANSFERS ##############################

    // Use for "moving" assets between vaults (asgard<>ygg), as well "churning" to a new Asgard
    function transferAllowance(address router, address newVault, address asset, uint amount, string memory memo) external nonReentrant {
        if (router == address(this)){
            _adjustAllowances(newVault, asset, amount);
            emit TransferAllowance(msg.sender, newVault, asset, amount, memo);
        } else {
            _routerDeposit(router, newVault, asset, amount, memo);
        }
    }

    //############################## ASSET TRANSFERS ##############################

    // Any vault calls to transfer any asset to any recipient.
    // Note: Contract recipients of ETH are only given 2300 Gas to complete execution.
    // Note: keep this function external scope to save gas for single transfers
    function transferOut(address payable to, address asset, uint amount, string calldata memo) external payable nonReentrant {
        uint safeAmount;
        if(asset == address(0)) {
            safeAmount = msg.value;
            bool success = to.send(safeAmount); // Send ETH. 
            if (!success) {
                payable(address(msg.sender)).transfer(safeAmount); // For failure, bounce back to Yggdrasil & continue.
            }
        } else {
            _vaultAllowance[msg.sender][asset] -= amount; // Reduce allowance
            (bool success, bytes memory data) = asset.call(abi.encodeWithSignature("transfer(address,uint256)" , to, amount));
            require(success && (data.length == 0 || abi.decode(data, (bool))));
            safeAmount = amount;
        }
        emit TransferOut(msg.sender, to, asset, safeAmount, memo);
    }

    // Similar to transferOut, but is private to allow looping over from this method
    // This method also doesn't "require" the transfer to succeed, allowing the loop to continue but failures should be handled/identified?
    function transferOutSingle(uint256 value, address payable to, address asset, uint amount) private nonReentrant returns (bool) {
        uint safeAmount;
        if (asset == address(0)) {
            bool success = to.send(value);
            if (!success) {
                payable(address(msg.sender)).transfer(safeAmount); // For failure, bounce back to Yggdrasil & continue.
            }
            return success;
        } else {
            _vaultAllowance[msg.sender][asset] -= amount; // Reduce allowance
            (bool success, bytes memory data) = asset.call(abi.encodeWithSignature("transfer(address,uint256)", to, amount));
            if (success && (data.length == 0 || abi.decode(data, (bool)))) {
                return true;
            } else {
                return false;
            }
        }
        // Note: Emitting events or additional logic can be added here based on success/failure
    }

    // potential optimization for batch outbound?
    // currently if one `transferOutSingle` fails, the others can continue
    function batchTransferOut(TransferDetails[] calldata transfers) external payable nonReentrant {
        for (uint i = 0; i < transfers.length; i++) {
            bool success = transferOutSingle(msg.value, transfers[i].to, transfers[i].asset, transfers[i].amount);
            if(success) {
                emit TransferOut(msg.sender, transfers[i].to, transfers[i].asset, transfers[i].amount, transfers[i].memo);
            } else {
                // what should be done?
            }
        }
    }

    // Any vault calls to transferAndCall on a target contract that conforms with "swapOut(address,uint256,address,address,uint256,uint256[])"
    // Example Memo: "~1b3:ETH.0xFinalToken:0xTo:"
    // Target is fuzzy-matched to the last three digits of whitelisted aggregators
    // toAsset, recipient, amountOutMin come from originating memo
    // Memo passed in here is the "OUT:HASH" type
    // RouterV5: transferOutAndCall can be used with ERC20 tokens as well as Ether
    // RouterV5: flags field can be passed from the originating memo like this thorchainMemo|flag1:flag2:flag3
    // RouterV5: Bifrost parses the flags and passes them here as an array of uint256
    // Note: keep this function external scope to save gas for single transferOutAndCalls
    function transferOutAndCall(address payable target, address fromAsset, uint256 fromAmount, address toAsset, address recipient, uint256 amountOutMin, string calldata memo, uint256[] calldata flags) external payable nonReentrant {
        uint256 _safeAmount;

        if(fromAsset == address(0)) {
            _safeAmount = msg.value;
            // call swapOut with ether
            (bool swapOutSuccess, ) = target.call{value:_safeAmount}(abi.encodeWithSignature("swapOut(address,uint256,address,address,uint256,uint256[])", fromAsset, fromAmount, toAsset, recipient, amountOutMin, flags));
            if(!swapOutSuccess) {
                bool sendSuccess = payable(recipient).send(_safeAmount); // If can't swap, just send the recipient the ETH
                if (!sendSuccess) {
                    payable(address(msg.sender)).transfer(_safeAmount); // For failure, bounce back to Yggdrasil & continue.
                }
            }
        } else {
            _safeAmount = fromAmount;
            _vaultAllowance[msg.sender][fromAsset] -= _safeAmount; // Reduce allowance
            safeApprove(fromAsset, target, 0); // best practice
            safeApprove(fromAsset, target, _safeAmount); // Approve target to spend asset

            // call swapOut with erc20
            (bool swapOutSuccess, ) = target.call{value:_safeAmount}(abi.encodeWithSignature("swapOut(address,uint256,address,address,uint256,uint256[])", fromAsset, _safeAmount, toAsset, recipient, amountOutMin, flags));
            
            if(!swapOutSuccess) {
                // call transfer with erc20
                (bool transferSuccess, bytes memory data) = fromAsset.call(abi.encodeWithSignature("transfer(address,uint256)" , recipient, _safeAmount));
                safeApprove(fromAsset, target, 0); // reset approval in all cases

                require(transferSuccess && (data.length == 0 || abi.decode(data, (bool))));
            } else {
                safeApprove(fromAsset, target, 0); // reset approval on successful swapOut
            }
        }
        
        emit TransferOutAndCall(msg.sender, target, _safeAmount, toAsset, recipient, amountOutMin, memo, flags);
    }

    function transferOutAndCallSingle(uint256 value, address payable target, address fromAsset, uint256 fromAmount, address toAsset, address recipient, uint256 amountOutMin, string calldata memo, uint256[] calldata flags) private nonReentrant returns (bool) {
        uint256 _safeAmount;

        if(fromAsset == address(0)) {
            _safeAmount = value;
            // call swapOut with ether
            (bool swapOutSuccess, ) = target.call{value:_safeAmount}(abi.encodeWithSignature("swapOut(address,uint256,address,address,uint256,uint256[])", fromAsset, fromAmount, toAsset, recipient, amountOutMin, flags));
            if(!swapOutSuccess) {
                bool sendSuccess = payable(recipient).send(_safeAmount); // If can't swap, just send the recipient the ETH
                if (!sendSuccess) {
                    payable(address(msg.sender)).transfer(_safeAmount); // For failure, bounce back to Yggdrasil & continue.
                }
            }
        } else {
            _safeAmount = fromAmount;
            _vaultAllowance[msg.sender][fromAsset] -= _safeAmount; // Reduce allowance
            safeApprove(fromAsset, target, 0); // best practice
            safeApprove(fromAsset, target, _safeAmount); // Approve target to spend asset

            // call swapOut with erc20
            (bool swapOutSuccess, ) = target.call{value:_safeAmount}(abi.encodeWithSignature("swapOut(address,uint256,address,address,uint256,uint256[])", fromAsset, _safeAmount, toAsset, recipient, amountOutMin, flags));
            
            if(!swapOutSuccess) {
                // call transfer with erc20
                (bool transferSuccess, bytes memory data) = fromAsset.call(abi.encodeWithSignature("transfer(address,uint256)" , recipient, _safeAmount));
                safeApprove(fromAsset, target, 0); // reset approval in all cases

                bool success = transferSuccess && (data.length == 0 || abi.decode(data, (bool)));

                return success;
            } else {
                safeApprove(fromAsset, target, 0); // reset approval on successful swapOut
            }
        }
        
        emit TransferOutAndCall(msg.sender, target, _safeAmount, toAsset, recipient, amountOutMin, memo, flags);
    }

    // potential optimization for batch outbound?
    function batchTransferOutAndCall(TransferAndCallDetails[] calldata transfers) external payable nonReentrant {
        for (uint i = 0; i < transfers.length; i++) {
            bool success = transferOutAndCallSingle(msg.value, transfers[i].target, transfers[i].fromAsset, transfers[i].fromAmount, transfers[i].toAsset, transfers[i].recipient, transfers[i].amountOutMin, transfers[i].memo, transfers[i].flags);
            
            if(!success) {
                // what should be done?
            }
        }
    }


    //############################## VAULT MANAGEMENT ##############################

    // A vault can call to "return" all assets to an asgard, including ETH. 
    function returnVaultAssets(address router, address payable asgard, Coin[] memory coins, string memory memo) external payable nonReentrant {
        if (router == address(this)){
            for(uint i = 0; i < coins.length; i++){
                _adjustAllowances(asgard, coins[i].asset, coins[i].amount);
            }
            emit VaultTransfer(msg.sender, asgard, coins, memo); // Does not include ETH.           
        } else {
            for(uint i = 0; i < coins.length; i++){
                _routerDeposit(router, asgard, coins[i].asset, coins[i].amount, memo);
            }
        }
        bool success = asgard.send(msg.value);
        require(success);
    }

    //############################## HELPERS ##############################

    function vaultAllowance(address vault, address token) public view returns(uint amount){
        return _vaultAllowance[vault][token];
    }

    // Safe transferFrom in case asset charges transfer fees
    function safeTransferFrom(address _asset, uint _amount) internal returns(uint amount) {
        uint _startBal = iERC20(_asset).balanceOf(address(this));
        (bool success, bytes memory data) = _asset.call(abi.encodeWithSignature("transferFrom(address,address,uint256)", msg.sender, address(this), _amount));
        require(success && (data.length == 0 || abi.decode(data, (bool))));
        return (iERC20(_asset).balanceOf(address(this)) - _startBal);
    }

    // Decrements and Increments Allowances between two vaults
    function _adjustAllowances(address _newVault, address _asset, uint _amount) internal {
        _vaultAllowance[msg.sender][_asset] -= _amount;
        _vaultAllowance[_newVault][_asset] += _amount;
    }

    // Adjust allowance and forwards funds to new router, credits allowance to desired vault
    function _routerDeposit(address _router, address _vault, address _asset, uint _amount, string memory _memo) internal {
        _vaultAllowance[msg.sender][_asset] -= _amount;
        (bool success,) = _asset.call(abi.encodeWithSignature("approve(address,uint256)", _router, _amount)); // Approve to transfer
        require(success);
        iROUTER(_router).depositWithExpiry(_vault, _asset, _amount, _memo, type(uint).max); // Transfer by depositing
    }

    function safeApprove(address _asset, address _address, uint _amount) internal {
        (bool success,) = _asset.call(abi.encodeWithSignature("approve(address,uint256)", _address, _amount)); // Approve to transfer
        require(success);
    }
}
