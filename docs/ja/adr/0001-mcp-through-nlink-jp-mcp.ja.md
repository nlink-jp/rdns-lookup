# ADR-0001: MCP のプロトコル層を nlink-jp/mcp にする — 新旧両方の時代を話す

| 項目 | 値 |
|-------|-----|
| Status | **Accepted**（2026-11-28） |
| Date | 2026-11-28 |
| Binds | rdns-lookup |
| Decision makers | nlink-jp maintainers |
| Triggered by | 組織 ADR-030（MCP の実装は新旧両方の時代を話し、プロトコル層は nlink-jp/mcp だけから取る）。issue nlink-jp/rdns-lookup#1 |

## Context

`internal/mcp/server.go` は手書きの stdio MCP サーバだった。旧方式（`initialize` の握手）だけを話すので、2026-07-28 版の
新方式専用のクライアントからはつながらない。rdns-lookup は家系 A（lookup 1 系統。nlink-jp/mcp の文書 server-migration §1）の
一員で、参照実装は asn-lookup である。asn-lookup の ADR-0001 には、2026-11-27 に測り直した実際の相手の記録もある
（Claude Code 2.1.311・gem-agent・lagent は、どれも両対応）。

## Decision

共通の方針（nlink-jp/mcp の `docs/ja/server-migration.ja.md`、v0.3.0）に、asn-lookup と同じく従う。

1. **外から見える形は変えない（方針 §2）。** `mcp.Serve` の引数を残す。ただし方針のシグナルの決まり（§2）のために、先頭に `ctx` を足す。中身は
   `server.ToolServer` で、`toolsList()` の順に登録し、各ツールのハンドラから `toolsCall(ctx, name, args)` を呼ぶ。
   手書きの読み書きのループ・握手・`rpcError` は消す。
2. **選択肢（方針 §2）**: `Workers` は 1、`QueueDepth` は 256、ログは stderr への slog（Warn 以上）。名前・版・
   instructions はいまのまま渡す。
3. **ツールの失敗（方針 §3）**: `decodeArgs` による厳格な解釈と、isError の文はいまのまま。引数の名前は正確に照合する
   ようになる。
4. **変わる振る舞い（方針 §4）**: CHANGELOG のとおり。initialize の前の `tools/*` と未知メソッドは -32602、オブジェクトで
   ない `arguments` は -32602、`"id":null` と `"jsonrpc"` の無い要求は -32600 になる。枠付けは `json.Decoder` から
   1 行 1 件（16 MiB）に変わる。parse error とバッチでは止まらなくなり、panic は -32603 で答える。実行中の呼び出しの
   後ろで待てるのは 256 件で、それを超えると -32603「server busy」になる。
5. **取り消し（方針 §5）**: `notifications/cancelled` で呼び出しの ctx が取り消されるようになる。ただしエンジンは ctx を受け取らないので、取り消された検索は最後まで動き、その答えは捨てられる。キャッシュは一時ファイルに丸ごと書いてから rename するので、半端なものは残らない。
6. **シグナル（方針 §2）**: `mcp` サブコマンドは、SIGINT と SIGTERM で取り消される ctx を `Serve` に渡す。
7. **依存と版**: `github.com/nlink-jp/mcp` を足す。minor を上げる（v0.4.0）。
8. **相手の代役（ADR-030 §6）**: `scripts/e2e.sh` の `printf` の流れは旧方式の相手の代役として残し、どれも先に `initialize` を送るようにした。その前の `tools/*` は断られるため。

## 見つけた不具合（それぞれ別の fix コミットで直した）

- 大文字小文字の違う引数名（`{"IP_Address": ...}` や、共通の引数の `Limit`）が、宣言された引数として受け付けられていた。名前を挙げて断るようにした。
- `mcp` が `context.Background()` で動いていたため、SIGINT・SIGTERM でプロセスが殺され、後片づけが走らなかった。シグナルで ctx を取り消すようにした。
- `scripts/e2e.sh` が、v0.2 で廃止したファイルへの書き出し（`workspace_root`・`records_file`）をまだ確かめていたため、通ることがなかった。`workspace_root` が名前を挙げて断られることと、記録がファイルを書かずに応答で返ることを確かめるようにした。
- `scripts/e2e.sh` の「索引に無い」確認が使う 203.0.113.9 に、いまは索引の記録がある。233.252.0.77（MCAST-TEST-NET）に替えた。

## Consequences

- 新方式専用のクライアントからもつながる。旧方式のクライアントには、これまでどおり答える。
- プロトコル層の更新はモジュールから届く。
- 試験は、要求をモジュールの `client` で流し直して動かす。プロトコルの細かな振る舞いの試験は外し（モジュールが試験している）、探りのクライアント 3 種の組み合わせを足した。

## Alternatives considered

| 案 | 採らない理由 |
|---|---|
| 手書きの層に `server/discover` を足す | ADR-030 §6 が禁じる、握手の写しが残る |
| `QueueDepth` を既定の 16 にする | 並べて送られた 17 件目が「server busy」で断られる。移行前は待たせていた |

## References

- 組織 ADR-030、ADR-025、ADR-021 §4
- nlink-jp/mcp v0.3.0: `docs/ja/server-migration.ja.md`
- asn-lookup ADR-0001（家系 A の参照実装）
- issue nlink-jp/rdns-lookup#1
