// SPDX-License-Identifier: MIT
// -------------------
// RevertingContract v1.0
// -------------------

pragma solidity 0.8.20;

contract RevertingContract {
    receive() external payable {
        revert();
    }
}
