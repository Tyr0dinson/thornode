package mock

import (
	"embed"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"

	_ "embed"
)

//go:embed data/*
var responses embed.FS

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func NewServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(">>>", r.URL.Path)
		switch r.URL.Path {
		case "/binance/api/v3/ticker/price":
			returnData("binance-ticker-price.json", w)
		case "/binance/api/v3/ticker/24hr":
			returnData("binance-ticker-24h.json", w)
		case "/binance/ws":
			binanceWsHandler(w, r)
		}
	}))
	// mux := http.NewServeMux()
	// mux.HandleFunc("/binance/api/v3/ticker/price", binanceTickerPriceHandler)
	// mux.HandleFunc("/binance/ws", binanceWsHandler)
	// return httptest.NewServer(mux)
}

func binanceTickerPriceHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println(r.URL.Path)
}

func binanceWsHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println(r.URL.Path)
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

		for _, filename := range []string{
			"binance-ws-sub-msg.json",
			"binance-ws-ticker-msg.json",
		} {
			data, _ := responses.ReadFile("data/" + filename)
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
