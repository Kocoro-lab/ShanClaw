const { execFileSync } = require("child_process");
const fs = require("fs");
const path = require("path");
const https = require("https");
const crypto = require("crypto");

const REPO = "Kocoro-lab/ShanClaw";
const BIN_DIR = path.join(__dirname, "bin");

function getPlatform() {
  const p = process.platform;
  if (p !== "darwin" && p !== "linux") {
    throw new Error("Unsupported platform: " + p + ". shan supports macOS and Linux.");
  }
  return p;
}

function getArch() {
  const map = { x64: "amd64", arm64: "arm64" };
  const a = map[process.arch];
  if (!a) {
    throw new Error("Unsupported architecture: " + process.arch);
  }
  return a;
}

function fetch(url) {
  return new Promise((resolve, reject) => {
    https.get(url, { headers: { "User-Agent": "shan-cli-npm" } }, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        return fetch(res.headers.location).then(resolve, reject);
      }
      if (res.statusCode !== 200) {
        return reject(new Error("HTTP " + res.statusCode + " for " + url));
      }
      const chunks = [];
      res.on("data", (c) => chunks.push(c));
      res.on("end", () => resolve(Buffer.concat(chunks)));
      res.on("error", reject);
    }).on("error", reject);
  });
}

async function main() {
  const platform = getPlatform();
  const arch = getArch();
  console.log("shan: detecting platform " + platform + "/" + arch);

  // Get latest release
  const releaseData = await fetch(
    "https://api.github.com/repos/" + REPO + "/releases/latest"
  );
  const release = JSON.parse(releaseData.toString());
  const version = release.tag_name.replace(/^v/, "");
  console.log("shan: installing v" + version + "...");

  const filename = "shan_" + version + "_" + platform + "_" + arch + ".tar.gz";
  const asset = release.assets.find((a) => a.name === filename);
  if (!asset) {
    throw new Error("No release asset found for " + filename);
  }

  // Download binary
  const tarball = await fetch(asset.browser_download_url);

  // Verify checksum
  const checksumAsset = release.assets.find((a) => a.name === "checksums.txt");
  if (checksumAsset) {
    const checksumData = await fetch(checksumAsset.browser_download_url);
    const line = checksumData.toString().split("\n").find((l) => l.includes(filename));
    if (line) {
      const expected = line.split(/\s+/)[0];
      const actual = crypto.createHash("sha256").update(tarball).digest("hex");
      if (actual !== expected) {
        throw new Error("Checksum mismatch for " + filename);
      }
      console.log("shan: checksum verified");
    }
  }

  // Extract with system tar
  fs.mkdirSync(BIN_DIR, { recursive: true });
  const tmpTar = path.join(BIN_DIR, "_shan.tar.gz");
  fs.writeFileSync(tmpTar, tarball);
  try {
    execFileSync("tar", ["-xzf", "_shan.tar.gz", "shan"], { cwd: BIN_DIR });
    // ax_server is only present in darwin archives — not an error on linux
    try {
      execFileSync("tar", ["-xzf", "_shan.tar.gz", "ax_server"], { cwd: BIN_DIR });
      fs.chmodSync(path.join(BIN_DIR, "ax_server"), 0o755);
    } catch (_) {}
  } finally {
    fs.unlinkSync(tmpTar);
  }

  fs.chmodSync(path.join(BIN_DIR, "shan"), 0o755);
  console.log("shan: v" + version + " installed successfully");
}

main().catch((err) => {
  console.error("shan install failed: " + err.message);
  process.exit(1);
});
