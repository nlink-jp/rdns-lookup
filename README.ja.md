# rdns-lookup

**IP に紐づくドメイン・ドメインのサブドメイン・CNAME 逆引きを調べる CLI 兼ローカル MCP サーバー。**

不審な IP やドメインを調査するとき、最初に知りたいのは周辺の関係です。この IP に他に何が載っているか、このドメインにどんなサブドメインがあるか、誰が CNAME でここを指しているか。`dig -x` では答えられません。PTR レコードは IP の持ち主が公開した 1 つの名前であって、実際にその IP へ A レコードを向けている数万のドメインは見えないからです。`1.1.1.1` の PTR は 1 件ですが、この索引には **83,216 件**あります。rdns-lookup は [THC](https://ip.thc.org) が無料公開する 60 億レコード規模の DNS 索引を引きます。第三者の索引を読むだけなので**調査対象へパケットを一切送らず**、トリアージの初手として最も安全な手段になります。

[asn-lookup](https://github.com/nlink-jp/asn-lookup)（帰属）・[whois-lookup](https://github.com/nlink-jp/whois-lookup)（登録情報）・[abuse-lookup](https://github.com/nlink-jp/abuse-lookup)（評判）・[doh-lookup](https://github.com/nlink-jp/doh-lookup)（現在の名前解決）・[urlscan-lookup](https://github.com/nlink-jp/urlscan-lookup)（URL 挙動）に並ぶ、**関係の広がり**にフォーカスした姉妹品です。**credential ゼロ・外部依存ゼロ。**

> **これは PTR ではありません。** 上流は "reverse DNS" と呼びますが、実体は PTR 名・その IP へ A レコードを向けているドメイン・証明書透明性ログ由来の名前を集約した索引です。答えるのは「他に何がここにあるか」です。持ち主が公開した権威ある 1 つの名前が知りたい場合は `doh-lookup` を使ってください。

## インストール

```bash
brew install nlink-jp/tap/rdns-lookup   # ビルド済み・Developer ID 署名 + notarize 済み・arm64 macOS
```

```bash
make build  # → dist/rdns-lookup
```

## 使い方

```bash
# この IP に他に何が載っているか
rdns-lookup rdns 142.251.43.46

# オクテット境界の CIDR ブロックも可（/8, /16, /24, /32 のみ）
rdns-lookup rdns 142.251.43.0/24

# 大量にヒットする IP は TLD や apex ドメインで絞る（単一アドレスのみ）
rdns-lookup rdns 142.251.43.46 --tld com,net
rdns-lookup rdns 142.251.43.46 --apex 1e100.com

# サブドメイン列挙。各レコードは最終観測日を持つ
rdns-lookup subdomains github.com

# このホスティング先を CNAME で指しているのは誰か
rdns-lookup cnames github.io

# 既定の 100 件より多く取得（上限 50000 件）
rdns-lookup rdns 1.1.1.1 --limit 500
rdns-lookup rdns 1.1.1.1 --all

# 機械可読出力。複数ターゲットのときは JSONL
rdns-lookup rdns --json 142.251.43.46
rdns-lookup rdns --json 142.251.43.46 8.8.8.8

# 一括入力：引数・ファイル・stdin。レート制限に合わせて自動ペーシング
rdns-lookup rdns --input targets.txt
cut -d, -f2 alerts.csv | rdns-lookup rdns --json

# キャッシュ
rdns-lookup cache status
rdns-lookup cache clear
```

実行例:

```
$ rdns-lookup rdns 142.251.43.46 --limit 4
142.251.43.46  [rdns]  4 records of 110 upstream, TRUNCATED
  source: ip.thc.org (json face), 1 request, rate budget 249/250 left
  note: upstream holds 110 records; 4 retrieved. Raise --limit or pass --all for more.
  2rkkem2jzi.com  (142.251.43.46, AS15169 GOOGLE, Queens, US)
  5mpqvdddy7.com  (142.251.43.46, AS15169 GOOGLE, Queens, US)
  65nijzgbc3.com  (142.251.43.46, AS15169 GOOGLE, Queens, US)
  bkk02s01-in-f14.1e100.net  (142.251.43.46, AS15169 GOOGLE, Queens, US)
```

すべての結果に「上流が保持する件数」と「実際に取得した件数」を併記するため、部分的な結果を完全な結果と誤認することがありません。

### 終了コード（rdns / subdomains / cnames）

| コード | 意味 |
|---|---|
| 0 | 1 件以上ヒットした |
| 1 | 全ターゲットで索引にヒットなし（失敗ではなく、これも答え） |
| 2 | エラー（不正な入力・ネットワーク障害・設定不備） |

## MCP サーバー

```bash
rdns-lookup mcp
```

ツール: `lookup_rdns` / `lookup_subdomains` / `lookup_cnames` / `cache_status` / `get_usage`。**まず `get_usage` を呼んでください** — ツールリファレンス、結果スキーマ、エラー回復表が返ります。ツールエラーは構造化 JSON（`{code, message}`）です。索引にヒットしないことは通常の結果であり、エラーではありません。200 件を超える結果は `workspace_root` 配下に JSONL で書き出し、パスと件数サマリのみを返すため、エージェントのコンテキストを溢れさせません。

Claude Code への登録:

```json
{
  "mcpServers": {
    "rdns-lookup": {
      "command": "rdns-lookup",
      "args": ["mcp"]
    }
  }
}
```

## 設定

**優先順位は フラグ > 環境変数 > 設定ファイル > 組み込み既定値。** 設定ファイルは任意です（[config.example.toml](config.example.toml) 参照）。

| 設定 | TOML | 環境変数 | 既定値 |
|---|---|---|---|
| API ルート | `[api] base_url` | `RDNS_LOOKUP_BASE_URL` | `https://ip.thc.org/api/v1` |
| 既定取得件数 | `[query] default_limit` | `RDNS_LOOKUP_DEFAULT_LIMIT` | `100` |
| `--all` の上限 | `[query] max_all` | `RDNS_LOOKUP_MAX_ALL` | `50000` |
| 重複除去 | `[query] dedup` | `RDNS_LOOKUP_DEDUP` | `true` |
| キャッシュ TTL（時間） | `[cache] ttl_hours` | `RDNS_LOOKUP_CACHE_TTL_HOURS` | `24` |
| キャッシュディレクトリ | `[cache] dir` | `RDNS_LOOKUP_CACHE_DIR` | `~/.cache/rdns-lookup` |
| ネットワークタイムアウト | `[network] timeout_seconds` | `RDNS_LOOKUP_TIMEOUT_SECONDS` | `30` |
| レート制限の下限 | `[ratelimit] min_remaining` | `RDNS_LOOKUP_MIN_REMAINING` | `20` |
| MCP インライン上限 | `[mcp] inline_max_records` | `RDNS_LOOKUP_MCP_INLINE_MAX` | `200` |
| MCP ワークスペース | `[mcp] workspace` | `RDNS_LOOKUP_WORKSPACE` | （なし） |

credential はどこにも登場しません。ip.thc.org に認証機構が存在しないためです。

## 「何が取れたか」を偽らない仕組み

これほど大きな索引では、打ち切られた結果を完全な結果と誤認しやすくなります。そうならないよう次の設計を入れています。

- **上流の総件数を必ず結果に添える。** `matching_records` を実取得件数の隣に置き、部分的な結果は `TRUNCATED` と表示して追加取得の方法を注記します。`matching_records` が 0 なのは「不明」（上流が非常に大きなブロックでは計数を諦める）であって「該当なし」ではありません。
- **重複を除去し、件数を報告する。** 上流は同じドメインを 2 回返すことがあり（`tld` が空のものと埋まったもの）、`/24` 検索では全体の約 15% に達します。除去時は情報量の多い側を残し、除去件数を明示します。
- **上流の 2 つの面を正しく使い分ける。** JSON 面はページングできますが `limit` を 100 で黙って打ち切ります。CSV 面は 50,000 件返せますがページングできません。要求件数から engine が選び、どちらが答えたかを結果に記録します。
- **効かないフィルタは拒否する。** 上流はブロック検索に `--tld`/`--apex` を付けても*エラーにせず黙って無視*し、絞り込まれていない全件を返します。本ツールはブロックとの併用をエラーにします。
- **不正なターゲットはネットワークに到達させない。** 上流にはオクテット境界の CIDR しか存在しないため、`/25` や `/29` は 406 を消費する前にローカルで説明付きで弾きます。

## 注意

- **単一ソース依存。** データはすべて ip.thc.org 由来です。同規模のデータセットを無料公開する事業者は他になく、上流の提供が止まれば本ツールも機能を失います。切り替え先を設定する仕組みはありません。
- **無料サービスへの礼儀。** 上流は全レスポンスに *"Free Service!, Do not abuse"* を付けて返します。24 時間キャッシュ・既定 100 件・`--all` の上限・一括実行時の自動ペーシング（レート制限の残数が下限を割ったら待つ）は、いずれもこれを守るための設計です。制限は 250 バースト、毎秒 0.5 で補充されます。
- **IPv6。** 入力は受け付けますが、上流に実質的な IPv6 データがないため、エラーではなく 0 件になります。
- **鮮度。** `subdomains` の結果は `last_seen_on` を持ちます。古い日付は「一度索引されたことがある」という意味で、今も解決するという意味ではありません。それが重要な場合は `doh-lookup` で確認してください。
- **名前解決との組み合わせ。** `subdomains` の出力を `doh-lookup` に流し、CNAME 先が NXDOMAIN のものを探せばサブドメインテイクオーバー候補が洗い出せます。ただしその手順は調査対象に触れるので、意図して実行してください。

## 開発

```bash
make build   # → dist/  （`go build` を直接使わない）
make test    # go test -race -cover ./...
make e2e     # 実 API に対するライブテスト（ネットワーク必要）
make check   # lint + test + build-all
```

## ライセンス

MIT — [LICENSE](LICENSE) を参照。
