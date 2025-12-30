package index

import "net/http"

// IndexHandlerFn 首頁回傳訊息
func IndexHandlerFn(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Welcome to ProbLab"))
}
