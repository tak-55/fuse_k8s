# リモートファイルシステム FUSE マウント (sshfs)

meta-fuse-csi-pluginを使用して、SSHリモートファイルシステムをKubernetes Pod内でFUSEマウントするサイドカーコンテナの実装です。

## アーキテクチャ

```
[sshfs sidecar]
  └─ touch /dev/fuse            (libfuse に fusermount3 経由を強制)
  └─ sshfs ... -f               (フォアグラウンド起動)
  └─ fusermount3-proxy          (fusermount3 として差し替え済み)
       └─ UDS ─────────────────> [CSI DaemonSet (CAP_SYS_ADMIN)]
                                     └─ open("/dev/fuse") + mount()
[app container]
  └─ /data (HostToContainer 伝播)
```

## 前提条件

1. Kubernetes v1.29+ (SidecarContainers 機能が必要)
2. meta-fuse-csi-plugin の CSI DaemonSet がデプロイ済み
   ```bash
   kubectl apply -f ../csi/csi-driver.yaml
   kubectl apply -f ../csi/csi-driver-daemonset.yaml
   ```

## セットアップ手順

### 1. sshfs サイドカーイメージのビルド & プッシュ

Dockerfile はマルチステージビルドで、Stage 1 で `fusermount3-proxy` を Go でビルドし、
Stage 2 で Ubuntu 22.04 ベースの sshfs イメージに組み込みます。

```bash
# Dockerfile 内の git clone が meta-fuse-csi-plugin リポジトリを自動取得するため、
# ソースの事前配置は不要

cd sshfs/
docker build -t sshfs-proxy:latest .
```

**レジストリにプッシュする場合:**

```bash
docker tag sshfs-proxy:latest your-registry/sshfs-proxy:latest
docker push your-registry/sshfs-proxy:latest
```

**kind を使う場合:**

```bash
kind load docker-image sshfs-proxy:latest
```

> kind の場合、deploy.yaml の `imagePullPolicy` が `Never` または `IfNotPresent` であることを確認してください。

### 2. SSH鍵の準備

SSH鍵ペアを生成し、公開鍵を接続先SSHサーバーに配置します。

```bash
# 鍵ペアの生成 (未作成の場合)
ssh-keygen -t ed25519 -f ~/.ssh/sshfs_key -N ""

# 公開鍵をSSHサーバーに配置
ssh-copy-id -i ~/.ssh/sshfs_key.pub user@your-ssh-server
```

秘密鍵をKubernetes Secretとして登録します。

```bash
kubectl create secret generic ssh-key \
  --from-file=private_key=${HOME}/.ssh/sshfs_key
```

### 3. デプロイマニフェストの編集

`deploy.yaml` の以下の環境変数を編集してください：

| 環境変数 | 説明 | 例 |
|---------|------|-----|
| `SSHFS_HOST` | SSH接続先ホスト | `192.168.72.27` |
| `SSHFS_USER` | SSHユーザー名 | `demouser` |
| `SSHFS_REMOTE_PATH` | リモートパス | `/home/demouser` |
| `SSHFS_PORT` | SSHポート番号 | `22` |

### 4. デプロイ

```bash
kubectl apply -f deploy.yaml
```

### 5. 動作確認

```bash
# Pod の起動確認
kubectl get pod sshfs-example

# マウント確認
kubectl exec -it sshfs-example -c app -- mount | grep fuse.sshfs

# ファイル一覧確認
kubectl exec -it sshfs-example -c app -- ls -la /data
```

## 環境変数

sshfs-proxy サイドカーは以下の環境変数で動作を制御できます：

| 変数名 | 必須 | デフォルト | 説明 |
|--------|------|-----------|------|
| `SSHFS_HOST` | ✓ | `localhost` | SSH接続先ホスト (IP/ホスト名) |
| `SSHFS_USER` | ✓ | `root` | SSHユーザー名 |
| `SSHFS_REMOTE_PATH` | | `/root/sshfs-example` | リモートマウントパス |
| `SSHFS_PORT` | | `22` | SSHポート番号 |
| `SSHFS_MOUNT_POINT` | | `/tmp` | マウント先パス |
| `USE_LOCAL_SSHD` | | `false` | `true`でコンテナ内sshdを起動 (デモ用) |
| `SSH_PRIVATE_KEY` | | - | SSH秘密鍵 (環境変数経由で注入する場合) |
| `FUSERMOUNT3PROXY_FDPASSING_SOCKPATH` | ✓ | `/var/lib/mfcp/uds/mfcp.sock` | UDSソケットパス |

## sshfs オプションのカスタマイズ

デフォルトで設定されているsshfsオプション：
- `-p ${SSHFS_PORT}` - SSHポート指定
- `-o StrictHostKeyChecking=no` - ホストキー検証スキップ
- `-o UserKnownHostsFile=/dev/null` - known_hostsファイル使用しない
- `-o IdentityFile=/secrets/ssh/private_key` - 秘密鍵ファイルパス
- `-f` - フォアグラウンド実行

entrypoint.sh を編集することで追加のオプションを指定できます。

## トラブルシューティング

### Pod が起動しない

```bash
# sshfs-proxy サイドカーのログを確認
kubectl logs sshfs-example -c sshfs-proxy

# CSI DaemonSet のログを確認
kubectl logs -n mfcp-system -l app=meta-fuse-csi-plugin
```

### マウントが成功しない

1. SSHサーバーへの接続確認：
   ```bash
   kubectl exec -it sshfs-example -c sshfs-proxy -- ssh -v ${SSHFS_USER}@${SSHFS_HOST}
   ```

2. SSH鍵の確認：
   ```bash
   kubectl get secret ssh-key -o yaml
   ```

3. リモートパスの存在確認：
   ```bash
   # SSH経由でリモートパスが存在するか確認
   ssh ${SSHFS_USER}@${SSHFS_HOST} "ls -la ${SSHFS_REMOTE_PATH}"
   ```

### 接続がタイムアウトする

- ネットワーク接続性の確認
- SSHサーバーのファイアウォール設定確認
- SSHポート番号が正しいか確認

## 注意事項

- sshfs は POSIX 互換ですが、通常のファイルシステムと完全に同一ではありません
- パフォーマンスはネットワークレイテンシに依存します
- 大量の小さいファイルの操作は遅くなる可能性があります
- 本番環境ではSSH鍵のパスフレーズ設定とホストキー検証を有効化することを推奨します

## 参考資料

- [meta-fuse-csi-plugin](https://github.com/pfnet-research/meta-fuse-csi-plugin)
- [sshfs](https://github.com/libfuse/sshfs)
