package contract

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
)

// A real accepted consume uses the unpublished unlock twice: LP at vout 0,
// funding at vout 2. The API deliberately cannot return that local parent.
func TestPoolConsumeUnlockUsesLocalParentForLPAndFunding(t *testing.T) {
	data, err := os.ReadFile("testdata/pool-locked-consume-parents.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Consume         string
		LocalUnlockTxid string
		Parents         map[string]string
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	parse := func(raw string) *bt.Tx {
		tx, e := bt.NewTxFromString(raw)
		if e != nil {
			t.Fatal(e)
		}
		return tx
	}
	current := parse(fixture.Consume)
	local := parse(fixture.Parents[fixture.LocalUnlockTxid])
	if hex.EncodeToString(current.Inputs[1].PreviousTxID()) != local.TxID() || current.Inputs[1].PreviousTxOutIndex != 0 {
		t.Fatal("LP must spend local unlock vout 0")
	}
	last := current.Inputs[len(current.Inputs)-1]
	if hex.EncodeToString(last.PreviousTxID()) != local.TxID() || last.PreviousTxOutIndex != 2 {
		t.Fatal("fee must spend local unlock vout 2")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/txraw/txid/")
		if id == local.TxID() {
			t.Error("queried unpublished unlock through API")
			http.NotFound(w, r)
			return
		}
		raw, ok := fixture.Parents[id]
		if !ok {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": "200", "data": map[string]string{"txraw": raw}})
	}))
	defer server.Close()
	key, _ := bec.PrivKeyFromBytes(bec.S256(), []byte{1}) // synthetic test signer
	p := NewPoolNFT2(&PoolNFT2Config{Network: server.URL + "/"})
	preID := hex.EncodeToString(current.Inputs[0].PreviousTxID())
	pre := parse(fixture.Parents[preID])
	grand := parse(fixture.Parents[hex.EncodeToString(pre.Inputs[0].PreviousTxID())])
	parents := make([]*bt.Tx, len(current.Inputs)-1)
	for i := 1; i < len(current.Inputs); i++ {
		parents[i-1] = parse(fixture.Parents[hex.EncodeToString(current.Inputs[i].PreviousTxID())])
	}
	expected, err := p.GetPoolNftUnlockOffLine(key, current, 0, pre, grand, parents, 1, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := p.getPoolNftUnlock(key, current, 0, preID, 0, 1, 2, 0, local)
	if err != nil {
		t.Fatal(err)
	}
	if actual.ToHex() != expected.ToHex() {
		t.Fatal("online/local parent resolution differs from offline signing")
	}
}
