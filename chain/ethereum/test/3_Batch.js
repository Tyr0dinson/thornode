const Router = artifacts.require("THORChain_Router");
const Aggregator = artifacts.require("THORChain_Aggregator");
const SushiRouter = artifacts.require("SushiRouterSmol");
const RevertingContract = artifacts.require("RevertingContract");
const Token = artifacts.require("ERC20Token");
const Weth = artifacts.require("WETH");
const BigNumber = require("bignumber.js");
const { expect } = require("chai");
const truffleAssert = require("truffle-assertions");
function BN2Str(BN) {
  return new BigNumber(BN).toFixed();
}

var ROUTER;
var ASGARD;
var AGG;
var WETH;
var SUSHIROUTER;
var REVERTINGCONTRACT;
var TOKEN;
var WETH;
var ETH = "0x0000000000000000000000000000000000000000";
var USER1;
var USER2;

const _1 = "1000000000000000000";
const _2 = "2000000000000000000";
const _3 = "3000000000000000000";
const _10 = "10000000000000000000";
const _20 = "20000000000000000000";
const _10k = "10000000000000000000000";
const _1m = "1000000000000000000000000";

const currentTime = Math.floor(Date.now() / 1000 + 15 * 60); // time plus 15 mins

describe("Batch Outbounds", function () {
  let accounts;

  before(async function () {
    accounts = await web3.eth.getAccounts();
    ROUTER = await Router.new();
    TOKEN = await Token.new(); // User gets 1m TOKENS during construction
    USER1 = accounts[0];
    USER2 = accounts[1];
    ASGARD = accounts[2];

    WETH = await Weth.new();
    SUSHIROUTER = await SushiRouter.new(WETH.address);
    AGG = await Aggregator.new(WETH.address, SUSHIROUTER.address);
    REVERTINGCONTRACT = await RevertingContract.new();
  });

  describe("Swap In and Out", function () {
    it("Should Deposit Assets to Router", async function () {
      await TOKEN.transfer(SUSHIROUTER.address, _10);

      let user1StartEth = BigNumber(await web3.eth.getBalance(USER1));
      let tx = await web3.eth.sendTransaction({
        to: SUSHIROUTER.address,
        from: USER1,
        value: _10,
      });
      let user1EndEth = BigNumber(await web3.eth.getBalance(USER1));
      // sanity check new balance net of send amount and gas
      expect(BN2Str(user1EndEth)).to.equal(
        BN2Str(
          user1StartEth
            .minus(
              BigNumber(tx.gasUsed).multipliedBy(
                BigNumber(tx.effectiveGasPrice),
              ),
            )
            .minus(_10),
        ),
      );

      await web3.eth.sendTransaction({
        to: WETH.address,
        from: USER1,
        value: _10,
      });

      await WETH.transfer(SUSHIROUTER.address, _10);
      await TOKEN.approve(ROUTER.address, _1m, { from: USER1 });
      await TOKEN.approve(ROUTER.address, _1m, { from: USER2 });
      await ROUTER.depositWithExpiry(
        ASGARD,
        TOKEN.address,
        _20,
        "SWAP:THOR.RUNE",
        currentTime,
        {
          from: USER1,
        },
      );

      expect(BN2Str(await TOKEN.balanceOf(SUSHIROUTER.address))).to.equal(_10);
      expect(BN2Str(await web3.eth.getBalance(SUSHIROUTER.address))).to.equal(
        _10,
      );
      expect(BN2Str(await WETH.balanceOf(SUSHIROUTER.address))).to.equal(_10);
      expect(BN2Str(await TOKEN.balanceOf(ROUTER.address))).to.equal(_20);
    });

    it("Should send 4 outbound transactions", async function () {
      let asgardStartEth = BigNumber(await web3.eth.getBalance(ASGARD));
      let user1StartEth = BigNumber(await web3.eth.getBalance(USER1));
      let user2StartEth = BigNumber(await web3.eth.getBalance(USER2));
      let user1StartToken = BigNumber(await TOKEN.balanceOf(USER1));
      let user2StartToken = BigNumber(await TOKEN.balanceOf(USER2));

      let tx = await ROUTER.batchTransferOutV5(
        [
          [USER1, ETH, _1, "OUT:ETH-USER1"],
          [USER1, ETH, _1, "OUT:ETH-USER1-AGAIN"],
          [USER2, ETH, _1, "OUT:ETH-USER2"],
          [USER2, TOKEN.address, _1, "OUT:TOKEN-USER2"],
        ],
        { from: ASGARD, value: _3 },
      );
      expect(tx.logs.length).to.equal(4);

      let asgardEndEth = BigNumber(await web3.eth.getBalance(ASGARD));
      expect(BN2Str(asgardEndEth)).to.equal(
        BN2Str(
          asgardStartEth
            .minus(
              BigNumber(tx.receipt.gasUsed).multipliedBy(
                BigNumber(tx.receipt.effectiveGasPrice),
              ),
            )
            .minus(_3),
        ),
      ); // Less 3 eth and some gas

      let user1EndEth = BigNumber(await web3.eth.getBalance(USER1));
      expect(BN2Str(user1EndEth)).to.equal(BN2Str(user1StartEth.plus(_2))); // 2x outbounds @ 1 ETH

      let user2EndEth = BigNumber(await web3.eth.getBalance(USER2));
      expect(BN2Str(user2EndEth)).to.equal(BN2Str(user2StartEth.plus(_1))); // 1 outbound @ 1 ETH

      let user1EndToken = BigNumber(await TOKEN.balanceOf(USER1));
      let user2EndToken = BigNumber(await TOKEN.balanceOf(USER2));
      expect(BN2Str(user1EndToken)).to.equal(BN2Str(user1StartToken)); // No change
      expect(BN2Str(user2EndToken)).to.equal(BN2Str(user2StartToken.plus(_1))); // 1 outbound @ 1 TOKEN
    });

    it("Should fail if not enough ether is sent", async function () {
      await truffleAssert.reverts(
        ROUTER.batchTransferOutV5(
          [
            [USER1, ETH, _1, "OUT:ETH-USER1"],
            [USER1, ETH, _1, "OUT:ETH-USER1-AGAIN"],
            [USER2, ETH, _1, "OUT:ETH-USER2"],
            [USER2, TOKEN.address, _1, "OUT:TOKEN-USER2"],
          ],
          { from: ASGARD, value: _2 },
        ),
        "Transaction reverted: function call failed to execute",
      );
    });

    it("Should send 2 dex agg outbounds", async function () {
      let asgardStartEth = BigNumber(await web3.eth.getBalance(ASGARD));
      let user1StartEth = BigNumber(await web3.eth.getBalance(USER1));
      let user2StartEth = BigNumber(await web3.eth.getBalance(USER2));
      let user1StartToken = BigNumber(await TOKEN.balanceOf(USER1));
      let user2StartToken = BigNumber(await TOKEN.balanceOf(USER2));

      let tx = await ROUTER.batchTransferOutAndCallV5(
        [
          [
            AGG.address,
            TOKEN.address,
            _1,
            ETH,
            USER1,
            "0",
            "OUT:HASH",
            "0x", // empty payload
            "bc123", // dummy address
          ],
          [
            AGG.address,
            ETH,
            _1,
            TOKEN.address,
            USER2,
            "0",
            "OUT:HASH",
            "0x", // empty payload
            "bc123", // dummy address
          ],
        ],
        { from: ASGARD, value: "0" },
      );

      expect(BN2Str(asgardEndEth)).to.equal(
        BN2Str(
          asgardStartEth.minus(
            BigNumber(tx.receipt.gasUsed).multipliedBy(
              BigNumber(tx.receipt.effectiveGasPrice),
            ),
          ),
        ),
      ); // Less gas

      let user1EndEth = BigNumber(await web3.eth.getBalance(USER1));
      expect(BN2Str(user1EndEth)).to.equal(BN2Str(user1StartEth.plus(_1))); // 1 outbound @ 1 ETH

      let user1EndToken = BigNumber(await TOKEN.balanceOf(USER1));
      expect(BN2Str(user1EndToken)).to.equal(BN2Str(user1StartToken)); // no change

      let user2EndEth = BigNumber(await web3.eth.getBalance(USER2));
      expect(BN2Str(user2EndEth)).to.equal(BN2Str(user2StartEth)); // no change

      let user2EndToken = BigNumber(await TOKEN.balanceOf(USER2));
      expect(BN2Str(user2EndToken)).to.equal(BN2Str(user2StartToken.plus(_1))); // 1 outbound @ 1 TOKEN
    });
  });
});
