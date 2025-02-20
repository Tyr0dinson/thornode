/*
 * This test deploys the ROUTER1 & TOKEN from User address
 * It then updates Asgard to an Asgard address
 * It then tests for User to Deposit in Assets, which gets forwarded to Asgard
 * It then tests for Asgard to send out those assets back to User
 */

const Router = artifacts.require("THORChain_Router");
const Token = artifacts.require("ERC20Token");
const EvilToken = artifacts.require("EvilERC20Token");
const StealToken = artifacts.require("StealERC20Token");
const RevertingContract = artifacts.require("RevertingContract");
const EvilCallback = artifacts.require("EvilCallback");
const UsdtToken = artifacts.require("TetherToken");

const BigNumber = require("bignumber.js");
const { expect } = require("chai");
const truffleAssert = require("truffle-assertions");

function BN2Str(BN) {
  return new BigNumber(BN).toFixed();
}
function getBN(BN) {
  return new BigNumber(BN);
}

var ROUTER1;
var TOKEN, EVILTOKEN, STEALTOKEN;
var ETH = "0x0000000000000000000000000000000000000000";
var USDT;
var REVERTINGCONTRACT, EVILCALLBACK;
var USER1, USER2;
var ASGARD1, ASGARD2, ASGARD3;

const _1 = "1000000000000000000";
const _10 = "10000000000000000000";
const _1000 = "1000000000000000000000";
const _50k = "50000000000000000000000";
const _250k = "250000000000000000000000";
const _500k = "500000000000000000000000";
const _1m = "1000000000000000000000000";

const currentTime = Math.floor(Date.now() / 1000 + 15 * 60); // time plus 15 mins

describe("Router contract", function () {
  let accounts;

  before(async function () {
    accounts = await web3.eth.getAccounts();
    ROUTER1 = await Router.new();
    TOKEN = await Token.new(); // User gets 1m TOKENS during construction
    EVILTOKEN = await EvilToken.new();
    REVERTINGCONTRACT = await RevertingContract.new();
    EVILCALLBACK = await EvilCallback.new(ROUTER1.address);
    USDT = await UsdtToken.new(_1m, "Tether", "USDT", 6);
    STEALTOKEN = await StealToken.new(TOKEN.address);
    USER1 = accounts[0];
    USER2 = accounts[1];
    ASGARD1 = accounts[2];
    ASGARD2 = accounts[3];
    ASGARD3 = accounts[4];
  });

  describe("User Deposit Assets", function () {
    it("Should Deposit Ether To Asgard1", async function () {
      let startBal = getBN(await web3.eth.getBalance(ASGARD1));
      let tx = await ROUTER1.depositWithExpiry(
        ASGARD1,
        ETH,
        _1000,
        "SWAP:THOR.RUNE",
        currentTime,
        { from: USER1, value: _1000 },
      );

      expect(tx.logs[0].event).to.equal("Deposit");
      expect(tx.logs[0].args.asset).to.equal(ETH);
      expect(BN2Str(tx.logs[0].args.amount)).to.equal(_1000);
      expect(tx.logs[0].args.memo).to.equal("SWAP:THOR.RUNE");

      let endBal = getBN(await web3.eth.getBalance(ASGARD1));
      let changeBal = BN2Str(endBal.minus(startBal));
      expect(changeBal).to.equal(_1000);
    });

    it("Should revert Deposit Ether To Asgard1", async function () {
      await truffleAssert.reverts(
        ROUTER1.depositWithExpiry(
          ASGARD1,
          ETH,
          _1000,
          "SWAP:THOR.RUNE",
          getBN(0),
          { from: USER1, value: _1000 },
        ),
        "THORChain_Router: expired",
      );
    });

    it("Should Deposit Token to Asgard1", async function () {
      expect(BN2Str(await TOKEN.totalSupply())).to.equal(_1m);
      expect(BN2Str(await TOKEN.balanceOf(USER1))).to.equal(_1m);

      // Approval - we are approving the ROUTER1 to spend all of USER1 funds
      await TOKEN.approve(ROUTER1.address, _1m, { from: USER1 });
      expect(BN2Str(await TOKEN.allowance(USER1, ROUTER1.address))).to.equal(
        _1m,
      );

      // tx
      let tx = await ROUTER1.depositWithExpiry(
        ASGARD1,
        TOKEN.address,
        _500k,
        "SWAP:THOR.RUNE",
        currentTime,
      );
      // console.log(tx.logs)
      expect(tx.logs[0].event).to.equal("Deposit");
      expect(tx.logs[0].args.asset).to.equal(TOKEN.address);
      expect(tx.logs[0].args.to).to.equal(ASGARD1);
      expect(BN2Str(tx.logs[0].args.amount)).to.equal(_500k);
      expect(tx.logs[0].args.memo).to.equal("SWAP:THOR.RUNE");

      expect(BN2Str(await TOKEN.balanceOf(ROUTER1.address))).to.equal(_500k);
      expect(BN2Str(await TOKEN.balanceOf(USER1))).to.equal(_500k);
      expect(
        BN2Str(await ROUTER1.vaultAllowance(ASGARD1, TOKEN.address)),
      ).to.equal(_500k);
    });

    it("Should Deposit StealToken to Asgard1", async function () {
      expect(BN2Str(await STEALTOKEN.balanceOf(USER1))).to.equal(_1m);

      // Approval - we are approving the ROUTER1 to spend all of USER1 funds
      await STEALTOKEN.approve(ROUTER1.address, _1m, { from: USER1 });
      expect(
        BN2Str(await STEALTOKEN.allowance(USER1, ROUTER1.address)),
      ).to.equal(_1m);

      await ROUTER1.depositWithExpiry(
        ASGARD1,
        STEALTOKEN.address,
        _500k,
        "SWAP:THOR.RUNE",
        currentTime,
      );

      expect(BN2Str(await STEALTOKEN.balanceOf(ROUTER1.address))).to.equal(
        _500k,
      );
      expect(BN2Str(await STEALTOKEN.balanceOf(USER1))).to.equal(_500k);
      expect(
        BN2Str(await ROUTER1.vaultAllowance(ASGARD1, STEALTOKEN.address)),
      ).to.equal(_500k);
    });

    it("Should revert Deposit Token to Asgard1", async function () {
      await truffleAssert.reverts(
        ROUTER1.depositWithExpiry(
          ASGARD1,
          TOKEN.address,
          _500k,
          "SWAP:THOR.RUNE",
          getBN(0),
        ),
        "THORChain_Router: expired",
      );
    });

    it("Should revert when ETH sent during ERC20 Deposit", async function () {
      await truffleAssert.reverts(
        ROUTER1.depositWithExpiry(
          ASGARD1,
          TOKEN.address,
          _1000,
          "MEMO",
          currentTime,
          { from: USER1, value: _1 },
        ),
        "unexpected eth",
      );
    });

    it("Should Deposit USDT to Asgard1", async function () {
      // await usdt.issue(_1m, { from: USER1 });
      expect(BN2Str(await USDT.totalSupply())).to.equal(_1m);
      expect(BN2Str(await USDT.balanceOf(USER1))).to.equal(_1m);

      // Approval - we are approving the ROUTER1 to spend all of USER1 funds
      await USDT.approve(ROUTER1.address, _1m, { from: USER1 });
      expect(BN2Str(await USDT.allowance(USER1, ROUTER1.address))).to.equal(
        _1m,
      );

      // tx
      let tx = await ROUTER1.depositWithExpiry(
        ASGARD1,
        USDT.address,
        _500k,
        "SWAP:THOR.RUNE",
        currentTime,
      );
      // console.log(tx.logs)
      expect(tx.logs[0].event).to.equal("Deposit");
      expect(tx.logs[0].args.asset).to.equal(USDT.address);
      expect(tx.logs[0].args.to).to.equal(ASGARD1);
      expect(BN2Str(tx.logs[0].args.amount)).to.equal(_500k);
      expect(tx.logs[0].args.memo).to.equal("SWAP:THOR.RUNE");

      expect(BN2Str(await USDT.balanceOf(ROUTER1.address))).to.equal(_500k);
      expect(BN2Str(await USDT.balanceOf(USER1))).to.equal(_500k);
      expect(
        BN2Str(await ROUTER1.vaultAllowance(ASGARD1, USDT.address)),
      ).to.equal(_500k);
    });
  });

  describe("Asgard Churns, Old Vaults can't spend", function () {
    it("Asgard Churns", async function () {
      // Router holds 500k of TOKEN1, allocated to ASGARD1
      expect(BN2Str(await TOKEN.balanceOf(ROUTER1.address))).to.equal(_500k);
      expect(
        BN2Str(await ROUTER1.vaultAllowance(ASGARD1, TOKEN.address)),
      ).to.equal(_500k);

      // First round, migrate half
      // Router should still hold 500k of TOKEN1, but now half is allocared to ASGARD2
      let tx1 = await ROUTER1.transferAllowance(
        ROUTER1.address,
        ASGARD2,
        TOKEN.address,
        _250k,
        "migrate:1",
        { from: ASGARD1 },
      );
      expect(tx1.logs[0].event).to.equal("TransferAllowance");
      expect(tx1.logs[0].args.asset).to.equal(TOKEN.address);
      expect(BN2Str(tx1.logs[0].args.amount)).to.equal(_250k);

      expect(BN2Str(await TOKEN.balanceOf(ROUTER1.address))).to.equal(_500k);
      expect(
        BN2Str(await ROUTER1.vaultAllowance(ASGARD1, TOKEN.address)),
      ).to.equal(_250k);
      expect(
        BN2Str(await ROUTER1.vaultAllowance(ASGARD2, TOKEN.address)),
      ).to.equal(_250k);

      // Second round, migrate remaining half
      // Router should still hold 500k of TOKEN1, but not all is allocated to ASGARD2
      let tx2 = await ROUTER1.transferAllowance(
        ROUTER1.address,
        ASGARD2,
        TOKEN.address,
        _250k,
        "migrate:2",
        { from: ASGARD1 },
      );
      expect(tx2.logs[0].event).to.equal("TransferAllowance");
      expect(tx2.logs[0].args.asset).to.equal(TOKEN.address);
      expect(BN2Str(tx2.logs[0].args.amount)).to.equal(_250k);

      expect(BN2Str(await TOKEN.balanceOf(ROUTER1.address))).to.equal(_500k);
      expect(
        BN2Str(await ROUTER1.vaultAllowance(ASGARD1, TOKEN.address)),
      ).to.equal("0");
      expect(
        BN2Str(await ROUTER1.vaultAllowance(ASGARD2, TOKEN.address)),
      ).to.equal(_500k);
    });

    it("Should fail to when old Asgard interacts", async function () {
      await truffleAssert.fails(
        ROUTER1.transferAllowance(
          ROUTER1.address,
          ASGARD2,
          TOKEN.address,
          _50k,
          "migrate:1234",
          { from: ASGARD1 },
        ),
        truffleAssert.ErrorType.REVERT,
      );
      await truffleAssert.fails(
        ROUTER1.transferOut(USER2, TOKEN.address, _50k, "OUT:", {
          from: ASGARD1,
        }),
        truffleAssert.ErrorType.REVERT,
      );
    });
  });

  describe("Upgrade contract", function () {
    it("should transfer all token and allowance to new contract", async function () {
      let asgard = accounts[5];
      let routerCurrent = await Router.new();
      let routerNext = await Router.new();

      // Approvals - current router can spend USER1 funds (for seeding)
      await TOKEN.approve(routerCurrent.address, _1m, { from: USER1 });
      await USDT.approve(routerCurrent.address, _1m, { from: USER1 });

      // Seed 1 ETH to asgard
      let asgardStartBalance = getBN(await web3.eth.getBalance(asgard));
      await routerCurrent.depositWithExpiry(
        asgard,
        ETH,
        _1,
        "SEED",
        currentTime,
        { from: accounts[10], value: _1 },
      );
      let asgardEndBalance = getBN(await web3.eth.getBalance(asgard));
      expect(BN2Str(asgardEndBalance.minus(asgardStartBalance))).to.equal(_1);

      // Seed 50k TOKEN to asgard on current router
      await routerCurrent.depositWithExpiry(
        asgard,
        TOKEN.address,
        _50k,
        "SEED",
        currentTime,
        { from: USER1 },
      );
      expect(
        BN2Str(await routerCurrent.vaultAllowance(asgard, TOKEN.address)),
      ).to.equal(_50k);
      expect(BN2Str(await TOKEN.balanceOf(routerCurrent.address))).to.equal(
        _50k,
      );

      // Migrate 50k TOKEN to new router contract
      let tx = await routerCurrent.transferAllowance(
        routerNext.address,
        asgard,
        TOKEN.address,
        _50k,
        "MIGRATE:1",
        { from: asgard },
      );

      expect(tx.logs[0].event).to.equal("Deposit");
      expect(tx.logs[0].args.to).to.equal(asgard);
      expect(tx.logs[0].args.asset).to.equal(TOKEN.address);
      expect(tx.logs[0].args.memo).to.equal("MIGRATE:1");
      expect(BN2Str(tx.logs[0].args.amount)).to.equal(_50k);

      // Make sure the token has been transferred and routerNext
      expect(BN2Str(await TOKEN.balanceOf(routerCurrent.address))).to.equal(
        "0",
      );
      expect(BN2Str(await TOKEN.balanceOf(routerNext.address))).to.equal(_50k);

      // Make sure the token vault allowance has moved from routerCurrent to routerNext
      expect(
        BN2Str(await routerNext.vaultAllowance(asgard, TOKEN.address)),
      ).to.equal(_50k);
      expect(
        BN2Str(await routerCurrent.vaultAllowance(asgard, TOKEN.address)),
      ).to.equal("0");
    });
  });

  describe("Evil callbacks", function () {
    it("should not give more allowance than tokens transferred", async function () {
      let startAllowance = getBN(
        await ROUTER1.vaultAllowance(USER1, EVILTOKEN.address),
      );
      await EVILTOKEN.approve(ROUTER1.address, _10, { from: USER1 });

      await truffleAssert.reverts(
        ROUTER1.depositWithExpiry(
          USER1,
          EVILTOKEN.address,
          _10,
          "",
          currentTime,
          { from: USER1 },
        ),
      );
    });

    it("Test transferOut reverting contract", async function () {
      let startEvilBal = await web3.eth.getBalance(REVERTINGCONTRACT.address);
      let tx = await ROUTER1.transferOut(
        REVERTINGCONTRACT.address,
        ETH,
        _10,
        "OUT:",
        { from: ASGARD1, value: _10 },
      );
      // Should emit a valid log ("fire and forget")
      expect(tx.logs[0].event).to.equal("TransferOutFail");
      expect(tx.logs[0].args.to).to.equal(REVERTINGCONTRACT.address);
      expect(tx.logs[0].args.asset).to.equal(ETH);
      expect(tx.logs[0].args.memo).to.equal("OUT:");
      expect(BN2Str(tx.logs[0].args.amount)).to.equal(_10);
      // The REVERTINGCONTRACT will have no ETH
      let endEvilBal = await web3.eth.getBalance(REVERTINGCONTRACT.address);
      expect(startEvilBal).to.equal(endEvilBal);
    });

    it("Test transferOut 'to' recipient tries re-entrancy", async function () {
      let startEvilBal = await web3.eth.getBalance(EVILCALLBACK.address);
      let tx = await ROUTER1.transferOut(
        EVILCALLBACK.address,
        ETH,
        _10,
        "OUT:",
        { from: ASGARD1, value: _10 },
      );
      // Should emit a valid log ("fire and forget")
      expect(tx.logs[0].event).to.equal("TransferOutFail");
      expect(tx.logs[0].args.to).to.equal(EVILCALLBACK.address);
      expect(tx.logs[0].args.asset).to.equal(ETH);
      expect(tx.logs[0].args.memo).to.equal("OUT:");
      expect(BN2Str(tx.logs[0].args.amount)).to.equal(_10);
      // The EVILCALLBACK will try to re-entrant but run out of gas, resulting in balance unchanged.
      let endEvilBal = await web3.eth.getBalance(EVILCALLBACK.address);
      expect(startEvilBal).to.equal(endEvilBal);
    });

    it("Test re-entrancy protection (generic)", async function () {
      await truffleAssert.fails(
        ROUTER1.transferAllowance(
          EVILCALLBACK.address,
          ASGARD1,
          ETH,
          0,
          "EvilReentrancy",
          { from: USER1 },
        ),
        truffleAssert.ErrorType.REVERT,
      );
    });
  });
});
