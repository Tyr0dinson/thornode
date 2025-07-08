package mock

import (
	"embed"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"

	_ "embed"
)

//go:embed data/*
var responses embed.FS

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func NewServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(">>>", r.URL.Path)

		// Websocket
		if strings.HasSuffix(r.URL.Path, "/ws") {
			handleWs(w, r)
		}

		// REST
		switch r.URL.Path {
		case "/binance/api/v3/ticker/price":
			returnData("binance-ticker-price.json", w)
		case "/binance/api/v3/ticker/24hr":
			returnData("binance-ticker-24h.json", w)
		case "/bitget/api/v2/spot/market/tickers":
			returnData("bitget-market-tickers.json", w)
		case "/bitmart/spot/quotation/v3/tickers":
			returnData("bitmart-quotation-tickers.json", w)
		case "/coinw/api/v1/public":
			returnData("coinw-ticker-response.json", w)
		}

	}))
}

func handleWs(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 3 {
		return
	}

	provider := parts[1]
	files := []string{}

	entries, err := responses.ReadDir("data")
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasPrefix(entry.Name(), provider+"-ws-") {
			continue
		}

		files = append(files, entry.Name())
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	for {
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}

		writeBogusWsMsg(conn)

		for _, filename := range files {
			data, err := responses.ReadFile("data/" + filename)
			if err != nil {
				fmt.Println(err)
				continue
			}
			_ = conn.WriteMessage(websocket.TextMessage, data)
		}
	}

}

func returnData(filename string, w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	data, _ := responses.ReadFile("data/" + filename)
	_, _ = w.Write(data)
}

func writeBogusWsMsg(conn *websocket.Conn) {
	_ = conn.WriteMessage(websocket.TextMessage, []byte("bogus"))
}
