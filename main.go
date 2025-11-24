package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"log"
)

// --- 設定定数 ---
const (
	baseURLPage  = "https://d-rw.com/Form/Product/ProductDetail.aspx?pid=%s"
	baseURLImage = "https://d-rw.com/Contents/ProductImages/0/%s_LL.jpg"
	baseURLSearch = "https://d-rw.com/Form/Product/ProductList.aspx?shop=0&cat=&dpcnt=51&img=2&sort=10&swrd=%s&udns=2&fpfl=0&sfl=0&pno=1"

	codeLength       = 5

	errorMessageSnippet = "商品が見つかりません"
    searchHitSnippet = `<ul class="itemList4">           <li>`

    // NOTE: モデル名抽出のためのJSON-LDスニペット
    jsonLdStartSnippet = `"description": "`
    modelStartSnippet  = "モデル…"

    // HTTPリクエストのタイムアウト設定
    httpTimeout = 5 * time.Second
)

// main: コマンドライン引数を受け取り、直列チェックを実行
func main() {
	log.SetFlags(log.Ldate | log.Ltime)

	if len(os.Args) != 3 {
		log.Println("エラー: 処理に必要な2つの引数 (開始ID, 検索範囲) を指定してください。")
		log.Println("使用方法: go run sequential_checker_v5.go [開始商品ID] [検索範囲(件数)]")
		log.Println("例: go run sequential_checker_v5.go ru98000 100")
		os.Exit(1)
	}

	startID := os.Args[1]
	rangeCountStr := os.Args[2]

	rangeCount, err := strconv.Atoi(rangeCountStr)
	if err != nil || rangeCount <= 0 {
		log.Fatalf("エラー: 検索範囲 '%s' は正の整数である必要があります。\n", rangeCountStr)
	}

	// 開始IDのプレフィックスと数字部分を分離
	prefix := ""
	startDigits := 0

	if len(startID) > codeLength {
		prefix = startID[:len(startID)-codeLength]
		startDigitsStr := startID[len(startID)-codeLength:]
		startDigits, err = strconv.Atoi(startDigitsStr)
		if err != nil {
			log.Fatalf("エラー: 商品ID '%s' の数字部分の解析に失敗しました。\n", startID)
		}
	} else {
		log.Fatalf("エラー: 商品ID '%s' は最低でも %d 文字（プレフィックスと %d 桁の数字）が必要です。\n", startID, codeLength+1, codeLength)
	}

	log.Printf("--- 探索開始: %s%0*d から %s%0*d までの %d 件を直列チェック ---",
		prefix, codeLength, startDigits,
		prefix, codeLength, startDigits + rangeCount - 1,
		rangeCount)

	// 2. 直列チェックの実行
	for i := 0; i < rangeCount; i++ {
		currentDigits := startDigits + i
		fullCode := prefix + fmt.Sprintf("%0*d", codeLength, currentDigits)

		pageURL := fmt.Sprintf(baseURLPage, fullCode)
		imageURL := fmt.Sprintf(baseURLImage, fullCode)
		searchURL := fmt.Sprintf(baseURLSearch, fullCode)

		// ページと画像のステータスを取得
		pageStatus, modelName := checkURL(pageURL, true)
		imageStatus, _ := checkURL(imageURL, false)

		// 検索ステータスを取得
		searchStatus := checkSearch(searchURL, fullCode)

		// シンプルなログ形式で出力: ID:ページ:画像:検索:モデル名
		fmt.Printf("%s:%s:%s:%s:%s\n", fullCode, pageStatus, imageStatus, searchStatus, modelName)

		// --- 検出ロジック１：廃盤/リダイレクト ---
		if pageStatus != "200" && imageStatus == "200" {
			fmt.Println("--検知 (廃盤/リダイレクト)")
			fmt.Printf("%s\n", pageURL)
			fmt.Printf("%s\n", imageURL)
			fmt.Println("--")
		}

		// --- 検出ロジック２：在庫切れ/非表示 ---
		if pageStatus == "200" && imageStatus == "200" && searchStatus != "200" {
			fmt.Println("--検知 (在庫切れ/非表示)")
			fmt.Printf("%s\n", pageURL)
			fmt.Printf("%s\n", imageURL)
			fmt.Printf("検索URL: %s\n", searchURL)
			fmt.Println("--")
		}
	}

	log.Println("--- チェック完了 ---")
}

// checkURL: URLにアクセスし、結果のステータスコード文字列とモデル名を返す
func checkURL(url string, isPageCheck bool) (string, string) {
	// 接続の再利用を禁止し、リダイレクト追跡を停止するクライアント
	tr := &http.Transport{
		DisableKeepAlives: true,
	}
	client := &http.Client{
		Timeout: httpTimeout,
        Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(url)
	if err != nil {
		return "-1", "" // ネットワークエラー
	}
	defer resp.Body.Close()

    statusCode := resp.StatusCode
    modelName := ""

    // ステータスコードが200 OK の場合
    if statusCode == http.StatusOK && isPageCheck {
		bodyBytes, err := io.ReadAll(resp.Body)
        if err != nil {
            return "-2", "" // ボディ読み込みエラー
        }
		bodyString := string(bodyBytes)

        // コンテンツエラーチェック (商品が見つかりません)
		if strings.Contains(bodyString, errorMessageSnippet) {
            return "404", ""
		}

        // --- モデル名抽出ロジック（JSON-LDから） ---
        jsonLdStart := strings.Index(bodyString, jsonLdStartSnippet)

        if jsonLdStart != -1 {
            descValueStart := jsonLdStart + len(jsonLdStartSnippet)
            modelStartRelative := strings.Index(bodyString[descValueStart:], modelStartSnippet)

            if modelStartRelative != -1 {
                modelStartAbsolute := descValueStart + modelStartRelative + len(modelStartSnippet)

                // モデル名後の " の位置を探す
                descValueEndRelative := strings.Index(bodyString[modelStartAbsolute:], `"`)

                if descValueEndRelative != -1 {
                    extracted := bodyString[modelStartAbsolute : modelStartAbsolute+descValueEndRelative]

                    // &lt;br&gt;（HTMLエンティティ）以降はモデル名ではないため削除
                    if brIndex := strings.Index(extracted, "&lt;br&gt;"); brIndex != -1 {
                        extracted = extracted[:brIndex]
                    }

                    modelName = strings.TrimSpace(extracted)
                }
            }
        }
        // --- ----------------- ---
	}

	return strconv.Itoa(statusCode), modelName
}

// checkSearch: 検索結果ページにアクセスし、商品がリストに含まれているかチェックする
func checkSearch(url string, productID string) string {
	tr := &http.Transport{
		DisableKeepAlives: true,
	}
	client := &http.Client{
		Timeout: httpTimeout,
        Transport: tr,
	}

	resp, err := client.Get(url)
	if err != nil {
		return "-1" // ネットワークエラー
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return strconv.Itoa(resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
    if err != nil {
        return "-2" // ボディ読み込みエラー
    }
	bodyString := string(bodyBytes)

	// HTML文字列に、商品リストの開始タグと <li> が含まれているかチェック
	if strings.Contains(bodyString, searchHitSnippet) {
        // 自分の商品IDがHTMLに含まれているか検証
        if strings.Contains(bodyString, fmt.Sprintf("/%s", productID)) {
            return "200" // 検索ヒットあり
        }
	}

	return "404" // 検索ヒットなし
}