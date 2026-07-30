# RFP: rdns-lookup

> Generated: 2026-07-30
> Status: Draft

## 1. Problem Statement

CTI/IR 実務者が不審な IP やドメインを調査するとき、最初に知りたいのは「この IP に他に何が載っているか」
「このドメインにどんなサブドメインが存在するか」「誰がこのドメインを CNAME で指しているか」という
**周辺関係**である。しかし既存手段では答えられない。`dig -x` や姉妹の `doh-lookup` が返す PTR は
「IP の持ち主が公開した 1 つの名前」でしかなく、実際にその IP へ A レコードを向けている数万の
ドメインは見えない（実測: `1.1.1.1` の PTR は `one.one.one.one` の 1 件だが、索引上は **83,216 件**）。
サブドメイン列挙と CNAME 逆引きに至っては、組織内のどのツールも手段を持たない。商用パッシブ DNS
（VirusTotal / SecurityTrails / Censys 等）は同等データを持つが、いずれも実務で常用するには高額である。

**rdns-lookup は、THC が無料公開する 60 億レコード規模の DNS 索引（ip.thc.org）に対して
「IP →ドメイン」「ドメイン→サブドメイン」「ドメイン→自分を指す CNAME」の 3 種の関係検索を行う**
CLI 兼ローカル MCP サーバーである。第三者の索引を引くだけなので**調査対象へパケットを一切送らない**。
これは実際に名前解決を行う `doh-lookup` の対極にあり、トリアージの初手として最も安全な手段になる。
対象ユーザーは、不審 IP / ドメインの周辺関係を OpSec を犠牲にせず把握したい CTI/IR 実務者。
`asn-lookup`（帰属）・`whois-lookup`（登録情報）・`abuse-lookup`（評判）・`doh-lookup`（現在の名前解決）に
並ぶ、**関係の広がり**にフォーカスした cybersecurity-series の姉妹品。

## 2. Functional Specification

### Commands / API Surface

**CLI サブコマンド**。上流の 3 エンドポイントに 1 対 1 で対応する 3 サブコマンド構成を採る。
ドメインを渡したとき「サブドメイン列挙」と「CNAME 逆引き」のどちらの問いなのかは入力から判別できないため、
`doh-lookup` の入力自動判定方式はここでは成立しない。

- `rdns-lookup rdns <ip|cidr...>` — IP / IP ブロックに紐づくドメイン
  - `--limit N` — 取得件数（既定 100）
  - `--all` — 上限まで全件取得（後述の 50,000 件天井まで）
  - `--tld com,net` — TLD で絞り込み（**単一 IP のみ**）
  - `--apex example.com` — apex ドメインで絞り込み（**単一 IP のみ**）
- `rdns-lookup subdomains <domain...>` — ドメインのサブドメイン列挙
  - `--limit N` / `--all`
- `rdns-lookup cnames <domain...>` — そのドメインを CNAME で指しているドメイン
  - `--limit N` / `--all`
- `rdns-lookup cache <status|clear>` — キャッシュの状態表示 / クリア
- `rdns-lookup mcp` — ローカル MCP サーバーを起動（stdio）
- `rdns-lookup --version`

全 lookup サブコマンド共通: `--json`（bulk 時は JSONL）/ `--raw`（上流生レスポンス）/ `--refresh`
（キャッシュ無視）/ `-c, --config` / 複数位置引数・stdin・`--input <file>` による bulk 入力。

**MCP ツール**:

- `lookup_rdns` — `{ ip_address, limit?, all?, tld?, apex_domain?, workspace_root? }`
- `lookup_subdomains` — `{ domain, limit?, all?, workspace_root? }`
- `lookup_cnames` — `{ target_domain, limit?, all?, workspace_root? }`
- `cache_status` — キャッシュ統計
- `get_usage` — ツールリファレンスとエラー回復表

### Input / Output

- **入力検証ゲート（ネットワーク I/O 前）**:
  - `rdns`: IP アドレス、または**オクテット境界の CIDR のみ**（`/8` `/16` `/24` `/32`）。
    上流は `/25` や `/29` を HTTP 406 で拒否するため、リクエスト前に明示エラーで弾く（CLI exit 2、
    MCP `{code:"invalid_input"}`）。
  - `subdomains` / `cnames`: RFC ホスト名検証（≤253 総長 / label 1–63 LDH / dot 必須 / 制御文字・CRLF 拒否）。
    IDN は punycode 変換（`doh-lookup` の RFC 3492 実装を移植）。
  - **`--tld` / `--apex` は単一 IP 指定時のみ許可**。上流は IP ブロックに対してフィルタを
    *エラーにせず黙って無視する*ため、そのまま通すと「絞り込んだつもりで全件返る」事故になる。
    ブロック指定との併用は CLI 側でエラーにする。
- **取得経路は内部で自動選択し利用者には見せない**。既定（〜100 件）は JSON API 1 回、
  `--all` または `--limit > 100` は CSV API（1 リクエスト 50,000 件）を使う。JSON API の `limit` は
  100 で黙って打ち切られるため、この切り替えは必須である。
- **50,000 件が実質天井**。CSV API にページングは無く、JSON API のページングで超過分を追うには
  0.5 req/sec 制限下で数百リクエストを要する。`--all` は 50,000 件で打ち止めとし、上流が返す
  `matching_records`（総ヒット数）と実取得件数を必ず併記して**切り捨てが起きたことを明示**する。
  それ以上を絞り込みたい場合は `--tld` / `--apex` を使う。
- **正規化（engine で一元化）**:
  - **重複除去を既定で行う**。上流は同一の `(domain, ip_address)` を `tld:""` と `tld:"net"` の
    2 行で返すことがある（実測 `/24` で 100 行中ユニーク 85 件、約 15% が重複）。
  - **CSV は必ず `hide_header=true` で取得し自前の列マッピングで読む**。上流の CSV ヘッダは
    1 列分回転していて（宣言 `ipAddress,apexDomain,subdomain,...` に対し実データは
    `apexDomain,subdomain,...,ipAddress`）、ヘッダを信じると全フィールドが誤ラベルになる。
  - `subdomains` の各行が持つ `last_seen_on` は保持して出力する（鮮度の判断材料になる）。
- **出力メタ**: すべての結果に**情報源（ip.thc.org）・取得経路（json / csv）・取得時刻・
  `matching_records` / 実取得件数 / 切り捨ての有無・レート制限残数**を明記する
  （`doh-lookup` が使用リゾルバを常に明記するのと同じ思想）。
- **用語の明示**: 上流が "reverse DNS" と呼ぶものは PTR ではなく、**PTR 名・その IP へ A レコードを
  向けているドメイン・CT ログ由来の名前を集約した索引**である。この差は出力ヘッダと usage / README に
  明記し、`doh-lookup` の PTR と混同されないようにする。
- **出力形式**: 人間可読（既定、bulk はターゲットごとにセクション）/ `--json`（bulk は JSONL）/ `--raw`。
- **Exit code 契約（lookup 系）**: `0` 1 件以上ヒット / `1` 全ターゲットでヒット 0 件（存在しないという
  成功回答）/ `2` 使用法・検証・ネットワークエラー。
- **MCP の大量結果はファイル経由**。閾値（既定 200 件）を超えたら `workspace_root` 配下に JSONL を
  書き出し、パスと件数サマリのみ返す（`urlscan-lookup` の `get_screenshot` / `abuse-lookup` の
  `get_reports` と同方式）。エージェントのコンテキストを溢れさせない。
- **bulk 入力はレート制限ペーシングを必須実装とする**。`x-ratelimit-remaining` を毎レスポンスで読み、
  残数が下限（既定 20）を下回ったら補充を待つ。積極リトライは行わない。

### Configuration

`~/.config/rdns-lookup/config.toml`（sectioned TOML、任意）。`RDNS_LOOKUP_*` 環境変数が上書き。
precedence は **フラグ > env > config > 既定**。

```toml
[api]
# base_url = "https://ip.thc.org/api/v1"

[query]
# default_limit = 100      # --limit / --all 省略時
# max_all = 50000          # --all の上限（上流 CSV API の上限）
# dedup = true             # 上流の重複行を除去

[cache]
# ttl_hours = 24           # 履歴索引なので変化は遅い
# dir = "~/.cache/rdns-lookup"

[network]
# timeout_seconds = 30

[ratelimit]
# min_remaining = 20       # 残数がこれを下回ったら補充を待つ
```

### External Dependencies

- **なし**（Go 標準ライブラリのみ）。`net/http` + `encoding/json` + `encoding/csv` で完結。
- 外部サービスは **ip.thc.org のみ**。**credential・API キー不要**（認証機構が存在しない）。

## 3. Design Decisions

- **言語 = Go、外部依存ゼロ。** シリーズ標準（asn / abuse / tor / whois / icloud-relay / doh / mac と同一）。
  単一署名バイナリで配布でき、CLI と MCP を 1 バイナリに同居させられる。
- **API 呼び出し + キャッシュ。バルクデータのローカル保持は採らない。** 上流は月次で全量を公開しているが
  最新版は parquet 圧縮 47GB / 展開 72GB・60 億レコードで、1 レコード検索でも実用的な応答時間にならない。
  API は認証不要で即答するため、キャッシュを噛ませれば十分である。
- **3 サブコマンド分割。** ドメインを渡したときの問いが「サブドメイン列挙」か「CNAME 逆引き」か入力から
  判別できないため、`doh-lookup` の自動判定方式は採らない。問いごとにコマンドを分けて曖昧さを消す。
- **JSON 面と CSV 面の使い分けは内部に隠蔽する。** 利用者が意識するのは件数だけで、どちらの
  エンドポイントを叩くかは engine が決める。上流の JSON `limit` 100 上限・CSV 50,000 上限という
  非対称は実装詳細に閉じ込める。
- **単一ソース依存を明示的に受容する。** 同等規模のデータセットを無料公開している事業者は他に無く、
  ip.thc.org の提供が止まればツールも機能を失う。代替ソースを抽象化する層は作らず（作っても差せる
  相手がいない）、README / AGENTS.md に単一ソース依存であることを明記する。`urlscan-lookup` が
  urlscan.io に依存するのと同じ割り切り。
- **無料サービスへの礼儀を設計に埋め込む。** 上流はレスポンス毎に "Free Service!, Do not abuse" を返す。
  キャッシュ既定 24 時間、bulk のレート制限ペーシング必須、積極リトライ禁止、`--all` の 50,000 件天井は
  いずれもこの方針の実装である。
- **engine は CLI / MCP で共有**し挙動を分岐させない。HTTP クライアントは注入インターフェイスで
  テスト時にモックする。
- **姉妹ツールとの関係**: `doh-lookup`（現在の名前解決・実際に問い合わせる）の対極として、
  **調査対象に触れない関係検索**を担う。返却されたドメイン / IP の enrichment は `asn-lookup`（帰属）・
  `whois-lookup`（登録情報）・`abuse-lookup`（評判）へ委譲（UNIX 哲学）。`subdomains` の結果を
  `doh-lookup` に流して CNAME 先が NXDOMAIN のものを探せばサブドメインテイクオーバー候補の
  洗い出しになるが、**その合成はツールに内蔵せず利用者側のパイプに委ねる**。
- **スコープ外（意図的）**:
  - **CT ログの日次 dump**（`cs2.ip.thc.org`、1 日約 400MB gz）— API ではなくバルクデータであり、
    別の問題領域。今回のスコープに含めない。
  - **月次バルク parquet / CSV のローカルクエリ** — 上記の理由により不採用
  - **IPv6** — 上流に実質データが無い（200 は返るが常に 0 件）。入力は受けるが 0 件になる旨を出力で説明する
  - **IP / ドメインの enrichment**（AS・評判・geo・登録情報）— 姉妹ツールへ委譲
  - **サブドメインテイクオーバーの自動判定** — v2 候補
  - 50,000 件を超える完全網羅取得 — レート制限下で非現実的

## 4. Development Plan

### Phase 1: Core（CLI）— 独立レビュー可

- `internal/query`: 入力分類（IP / CIDR / ドメイン）+ 検証ゲート（オクテット境界 CIDR のみ、
  RFC ホスト名検証）、IDN punycode（`doh-lookup` から移植）
- `internal/config`: sectioned TOML + `RDNS_LOOKUP_*` env + フラグ（precedence 適用）
- `internal/cache`: 固定 TTL キャッシュ（`whois-lookup` 方式の `fetched_at_unix` + `Get(key, now, ttl)`）、
  atomic write
- `internal/thc`: 上流クライアント。JSON 面 3 本 + CSV 面 3 本、HTTP 注入インターフェイス、
  `hide_header=true` 固定と自前列マッピング、`x-ratelimit-*` パース、406 の構造化エラー解釈
- `internal/engine`: validate → 経路選択（json / csv）→ cache → fetch → 正規化（dedup・
  `matching_records` と切り捨て判定）→ レート制限ペーシング
- `internal/app`: `rdns` / `subdomains` / `cnames` / `cache` サブコマンド、`--limit`/`--all`/`--tld`/
  `--apex`/`--json`/`--raw`/`--refresh`、bulk 入力（複数引数 + stdin/`--input`）、出力メタ明記、
  exit code 契約
- モック HTTP でのテーブル駆動テスト一式 + `httptest.Server` による full CLI パス統合テスト

### Phase 2: Features（MCP）— 独立レビュー可

- `internal/mcp`: zero-dep stdio JSON-RPC 2.0、ツール `lookup_rdns` / `lookup_subdomains` /
  `lookup_cnames` / `cache_status` / `get_usage`、構造化エラー `{code, message}`
- `internal/workspace`: 閾値超過結果の JSONL ファイル出力（`workspace_root`、`os.OpenRoot` による
  封じ込め）
- `usage.md` + ツール名 / エラーコードの整合を固定するメタテスト 2 本

### Phase 3: Release

- README.md / README.ja.md / CHANGELOG.md / AGENTS.md / config.example.toml / LICENSE
- Makefile + scripts（codesign / notarize / brew）、build-all（linux amd64/arm64・darwin arm64・
  windows amd64）、darwin 署名 + notarize、homebrew-tap formula
- 実データ E2E（`//go:build e2e` + `scripts/e2e.sh`）
- submodule 統合 → org profile + web-site catalog 同期 → check-org.sh

## 5. Required API Scopes / Permissions

**None.** ip.thc.org の API には認証機構が存在せず、API キー・OAuth スコープ・IAM ロールは一切不要。
レート制限のみが実質的なアクセス制御である（IP ベース、250 バースト + 毎秒 0.5 補充）。

## 6. Series Placement

Series: **cybersecurity-series**
Reason: 不審 IP / ドメインの周辺関係を調査対象に触れずに収集する CTI/IR 支援ツールであり、
`asn-lookup` / `abuse-lookup` / `tor-exit-lookup` / `whois-lookup` / `icloud-relay-lookup` /
`doh-lookup` / `mac-lookup` と同じ「CLI 兼 MCP・credential ゼロ・外部依存ゼロの調査ルックアップ」
ファミリーに属する。

## 7. External Platform Constraints

上流仕様は 2026-07-30 に実測で確認した。以下はドキュメント未記載の挙動を含む。

- **レート制限**: `x-ratelimit-limit: 250`（バースト）+ `x-ratelimit-rate: 0.50`（req/sec で補充）、
  残数は `x-ratelimit-remaining`。IP ベース。レスポンス body に毎回 `"comment":"Free Service!, Do not abuse"`。
- **JSON API の `limit` は 100 で黙って打ち切られる**。1000 でも 50000 でも 100 件しか返らない。
  ページングは不透明トークン `next_page_state` を次リクエストの `page_state` に渡す方式。
- **CSV API の `limit` は 50,000**。ページング機構は無い。
- **CSV のヘッダ行が 1 列分回転している**。宣言は
  `ipAddress,apexDomain,subdomain,tld,country,city,asn,organization` だが実データは
  `apexDomain,subdomain,tld,country,city,asn,organization,ipAddress`（`ipAddress` が末尾）。
  `hide_header=true` を必須とし自前マッピングで読む。
- **重複レコードが混じる**。同一 `(domain, ip_address)` が `tld:""` と `tld:"net"` の 2 行で返る。
  `/24` 検索で 100 行中ユニーク 85 件（約 15%）。
- **CIDR はオクテット境界のみ**。`/8` `/16` `/24` `/32` は通るが `/25` `/29` は HTTP 406
  `{"status":"error","error":"invalid ip"}`。
- **CIDR に `tld` / `apex_domain` フィルタを付けるとエラーにならず黙って無視される**。
  ドキュメントは「ブロックには使えない」と書くのみで失敗させない。
- **docs のレスポンススキーマが実装と不一致**。subdomains はドキュメントが `subdomains` キーと
  書くが実際は `domains`。strict decode するなら実測に合わせる。
- **IPv6 は実質データなし**。200 + `domains:[]` + `matching_records:0` +
  `"Could not fetch result count, matching_records will be zero, this can happen for /8 blocks"`。
- データなしは 200 + 空配列（エラーではない）。不正入力は 406 + `{"status":"error","error":"..."}`。
- `matching_records` に総ヒット数が入るため、切り捨て判定と件数見積りに使える。
- **単一ソース依存**: 同規模のデータセットを無料公開する代替事業者は確認できていない。上流が停止すれば
  ツールは機能を失う。

---

## Discussion Log

- **発端**: ユーザーが ip.thc.org を発見し、lookup シリーズ（CLI + MCP）に加えて強化したいと提案。
  60GB 超のバルクデータをローカルでクエリするのは非現実的（1 レコード検索でも数分）との判断から、
  **API 呼び出し + キャッシュ**が妥当と提案された。調査で最新バルクは parquet 圧縮 47GB / 展開 72GB・
  60 億レコードであることを確認し、この判断を裏付けた。
- **API ドキュメントの実地調査**: docs が SPA のため WebFetch では内容が取れず、ブラウザでレンダリングして
  読み取った。結果、ユーザーが挙げた 3 ページ（reverse-dns / subdomain / cname）に加え、**同一データの
  CSV ダウンロード API が 3 本存在**することを発見（`limit` 上限 50,000）。
- **`doh-lookup` 拡張ではなく新規プロジェクトとする判断**: THC が "reverse DNS" と呼ぶものは PTR ではなく
  「その IP に紐づくと観測された全ドメイン」の集約索引である。実測で `1.1.1.1` は PTR 1 件に対し索引
  83,216 件。答える問いが別物であり、データソースの性質（履歴索引 vs 現在の権威回答）も異なるため、
  独立した姉妹ツールとする。
- **命名**: `rdns-lookup` と `thc-lookup`（`urlscan-lookup` 同様のベンダー名方式）を比較。単一ソース依存が
  完全であることは事実だが、カタログで機能が一目で分かる `rdns-lookup` を採用。上流が停止すれば
  ツールごと停止する点は README / AGENTS.md に明記する方針で合意。
- **CT ログはスコープ外で合意**: `cs2.ip.thc.org` の日次 dump（約 400MB gz/日）は API ではなくバルクデータ
  であり、別の問題領域として今回は含めない。
- **実測で 9 件の落とし穴を発見**（項目 7 に記載）。特に設計に直結したのは (a) JSON `limit` の 100 上限、
  (b) CSV ヘッダの列ずれ、(c) 約 15% の重複レコード、(d) CIDR のオクテット境界制限とフィルタの
  silent ignore の 4 件。いずれもドキュメント未記載。プローブ結果はレート制限を消費して得たため
  メモリ（`reference_ip_thc_org_api`）に保存済み。
- **機能仕様の決定（4 点、いずれも推奨案を採用）**:
  - **3 サブコマンド分割**（`rdns` / `subdomains` / `cnames`）— ドメイン入力ではサブドメイン列挙と
    CNAME 逆引きが判別不能なため、`doh-lookup` の自動判定方式は不成立
  - **既定 100 件 + `--all` で全件** — `1.1.1.1` は 83,216 件ヒットするため、暗黙の全件取得は
    レート制限を溶かす事故になる
  - **MCP の大量結果は閾値超過でファイル経由**（`urlscan-lookup` の `workspace_root` パターン）
  - **bulk 入力を v1 に含める + ペーシング必須** — `x-ratelimit-remaining` を見た自動ペーシングを
    同時実装し、無料サービスに対して礼を欠かない挙動にする
- **50,000 件天井の扱い**: CSV にページングが無く、JSON ページングで 83,216 件を追うには 0.5 req/sec 下で
  数百リクエスト（十数分）を要するため非現実的。`--all` は 50,000 件で打ち止めとし、`matching_records` と
  実取得件数を併記して切り捨てを明示する方針とした。絞り込みたい場合は `--tld` / `--apex` を使う。
- **キャッシュ TTL**: 履歴索引で変化が遅いため既定 24 時間、`whois-lookup` と同じ時間単位設定を採用。
  上流の `last_seen_on` に直近日付（`github.com` のサブドメインに 2026-07-17）が見られたため短縮も検討したが、
  **上流データセット自体がそこまで高速に更新されているわけではない**との判断で 24 時間のまま確定（ユーザー確認済）。
- **RFP 確定**: 上記 TTL の確認をもって全 7 項目が確定。Phase 2（スキャフォールド）へ進行。
