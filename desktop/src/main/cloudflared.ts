import { createHash, randomUUID } from 'node:crypto';
import { constants } from 'node:fs';
import {
  access,
  chmod,
  mkdir,
  readFile,
  rename,
  rm,
  writeFile,
} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { x as extract } from 'tar';

interface ReleaseAsset {
  name: string;
  browser_download_url: string;
  digest: string | null;
}

export class CloudflaredManager {
  private pending?: Promise<string>;

  constructor(private userData: string) {}

  get binaryPath(): string {
    return path.join(
      this.userData,
      'bin',
      process.platform === 'win32' ? 'cloudflared.exe' : 'cloudflared',
    );
  }

  async findBinary(): Promise<string | undefined> {
    const filename =
      process.platform === 'win32' ? 'cloudflared.exe' : 'cloudflared';
    const candidates = [
      process.env.REMOTE_CODEX_CLOUDFLARED,
      ...String(process.env.PATH ?? '')
        .split(path.delimiter)
        .filter(Boolean)
        .map((directory) => path.join(directory, filename)),
      path.join(this.userData, 'bin', filename),
      '/opt/homebrew/bin/cloudflared',
      '/usr/local/bin/cloudflared',
      ...['ProgramFiles', 'ProgramFiles(x86)', 'LOCALAPPDATA'].flatMap((key) =>
        process.env[key]
          ? [path.join(process.env[key]!, 'cloudflared', filename)]
          : [],
      ),
      path.join(
        process.env.LOCALAPPDATA ?? os.homedir(),
        'Microsoft',
        'WinGet',
        'Links',
        filename,
      ),
      path.join(
        process.env.USERPROFILE ?? os.homedir(),
        'scoop',
        'shims',
        filename,
      ),
      path.join(
        process.env.ChocolateyInstall ?? 'C:\\ProgramData\\chocolatey',
        'bin',
        filename,
      ),
    ];
    for (const candidate of candidates) {
      if (!candidate) continue;
      try {
        await access(
          candidate,
          process.platform === 'win32' ? constants.F_OK : constants.X_OK,
        );
        return candidate;
      } catch (error) {
        const code = (error as NodeJS.ErrnoException).code;
        if (code !== 'ENOENT' && code !== 'EACCES' && code !== 'ENOTDIR')
          throw error;
      }
    }
    return undefined;
  }

  ensureBinary(): Promise<string> {
    this.pending ??= this.install().finally(() => {
      this.pending = undefined;
    });
    return this.pending;
  }

  private async install(): Promise<string> {
    const existing = await this.findBinary();
    if (existing) return existing;
    const platform =
      process.platform === 'win32' ? 'windows' : process.platform;
    const arch = process.arch === 'x64' ? 'amd64' : process.arch;
    if (
      !['darwin-arm64', 'darwin-amd64', 'windows-amd64'].includes(
        `${platform}-${arch}`,
      )
    ) {
      throw new Error(
        '当前平台没有可下载的 cloudflared，请在控制面板中选择固定域名模式。',
      );
    }
    const name = `cloudflared-${platform}-${arch}.${platform === 'windows' ? 'exe' : 'tgz'}`;
    const headers = {
      'User-Agent': 'Remote-Codex',
      Accept: 'application/vnd.github+json',
    };
    const releaseResponse = await fetch(
      'https://api.github.com/repos/cloudflare/cloudflared/releases/latest',
      { headers, signal: AbortSignal.timeout(20_000) },
    );
    if (!releaseResponse.ok)
      throw new Error(
        `cloudflared 版本查询失败（HTTP ${releaseResponse.status}）。可在托盘菜单中重试下载，或使用固定域名模式。`,
      );
    const release = (await releaseResponse.json()) as {
      assets: ReleaseAsset[];
    };
    const asset = release.assets.find((item) => item.name === name);
    if (
      !asset ||
      !asset.browser_download_url.startsWith(
        'https://github.com/cloudflare/cloudflared/releases/download/',
      )
    ) {
      throw new Error(
        '官方发布中缺少匹配的 cloudflared 文件。可使用固定域名模式。',
      );
    }
    if (!asset.digest?.startsWith('sha256:'))
      throw new Error('官方发布缺少 SHA-256 校验信息，下载已停止。');
    const response = await fetch(asset.browser_download_url, {
      signal: AbortSignal.timeout(120_000),
    });
    if (!response.ok)
      throw new Error(
        `cloudflared 下载失败（HTTP ${response.status}）。请在托盘菜单中重试。`,
      );
    const chunks: Uint8Array[] = [];
    let size = 0;
    if (!response.body) throw new Error('CLOUDFLARED_DOWNLOAD_EMPTY');
    for await (const chunk of response.body) {
      size += chunk.length;
      if (size > 100 * 1024 * 1024)
        throw new Error('CLOUDFLARED_DOWNLOAD_TOO_LARGE');
      chunks.push(chunk);
    }
    const data = Buffer.concat(chunks);
    if (
      `sha256:${createHash('sha256').update(data).digest('hex')}` !==
      asset.digest
    )
      throw new Error('cloudflared 文件校验失败，请重试下载。');
    const directory = path.join(this.userData, 'bin');
    const staging = path.join(directory, `.download-${randomUUID()}`);
    await mkdir(staging, { recursive: true, mode: 0o700 });
    try {
      const filename =
        platform === 'windows' ? 'cloudflared.exe' : 'cloudflared';
      const source = path.join(staging, filename);
      if (platform === 'windows')
        await writeFile(source, data, { flag: 'wx', mode: 0o700 });
      else {
        const archive = path.join(staging, 'cloudflared.tgz');
        await writeFile(archive, data, { flag: 'wx', mode: 0o600 });
        await extract({
          file: archive,
          cwd: staging,
          strict: true,
          filter: (entry, stat) =>
            entry === 'cloudflared' && 'type' in stat && stat.type === 'File',
        });
      }
      if ((await readFile(source)).length === 0)
        throw new Error('CLOUDFLARED_BINARY_EMPTY');
      if (platform !== 'windows') await chmod(source, 0o700);
      const target = path.join(directory, filename);
      await rename(source, target);
      return target;
    } finally {
      await rm(staging, { recursive: true, force: true });
    }
  }
}
