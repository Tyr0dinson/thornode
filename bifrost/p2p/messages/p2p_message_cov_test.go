package messages

import "testing"

func TestTHORChainTSSMessageTypeStringAllCov(t *testing.T) {
	cases := map[THORChainTSSMessageType]string{
		TSSKeyGenMsg:          "TSSKeyGenMsg",
		TSSKeySignMsg:         "TSSKeySignMsg",
		TSSKeyGenVerMsg:       "TSSKeyGenVerMsg",
		TSSKeySignVerMsg:      "TSSKeySignVerMsg",
		TSSFrostPreprocessMsg: "TSSFrostPreprocessMsg",
		TSSFrostShareMsg:      "TSSFrostShareMsg",
		TSSFrostVerifyMsg:     "TSSFrostVerifyMsg",
		TSSFrostResultMsg:     "TSSFrostResultMsg",
		TSSFrostDKGRound1Msg:  "TSSFrostDKGRound1Msg",
		TSSFrostDKGRound2Msg:  "TSSFrostDKGRound2Msg",
		TSSFrostDKGRound3Msg:  "TSSFrostDKGRound3Msg",
		TSSFrostDKGResultMsg:  "TSSFrostDKGResultMsg",
		// control / task-done / Unknown / out-of-range have no dedicated name
		TSSControlMsg:                "Unknown",
		TSSTaskDone:                  "Unknown",
		Unknown:                      "Unknown",
		THORChainTSSMessageType(250): "Unknown",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Fatalf("type %d: got %q want %q", k, got, want)
		}
	}
}
