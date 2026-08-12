import { build } from "esbuild";
import { readdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const watchDir = path.dirname(fileURLToPath(import.meta.resolve("@moq/watch/element")));
const sourceName = (await readdir(watchDir)).find((name) => /^source-.*\.js$/.test(name));
if (!sourceName) throw new Error("@moq/watch source bundle was not found");

const sourcePath = path.join(watchDir, sourceName);
const originalSource = await readFile(sourcePath, "utf8");
let source = originalSource;
const subscribeNeedle = "let t = this.broadcast.track(this.track).subscribe({ priority: s.PRIORITY.video });";
const subscribeReplacement = "let g = globalThis.__NCDN_PLAYER_GENERATION, t = this.broadcast.track(this.track).subscribe({ priority: s.PRIORITY.video, ordered: globalThis.__NCDN_REWIND_START_GROUP !== void 0, latencyMax: 30000, startGroup: globalThis.__NCDN_REWIND_START_GROUP });";
const subscribeOffset = source.indexOf(subscribeNeedle);
if (subscribeOffset < 0) throw new Error("@moq/watch video subscription hook changed");
source = source.slice(0, subscribeOffset) + source.slice(subscribeOffset).replace(subscribeNeedle, subscribeReplacement);

const groupNeedle = "let { frame: t } = e;";
const groupReplacement = "globalThis.dispatchEvent(new CustomEvent(\"ncdn-moq-group\", { detail: { group: e.group, generation: g } })); let { frame: t } = e;";
const searchOffset = subscribeOffset;
const groupOffset = source.indexOf(groupNeedle, searchOffset);
if (groupOffset < 0) throw new Error("@moq/watch video group hook changed");
source = source.slice(0, groupOffset) + source.slice(groupOffset).replace(groupNeedle, groupReplacement);
await writeFile(sourcePath, source);

try {
  await build({
    entryPoints: {
      "watch-element": "@moq/watch/element",
      "watch-ui": "@moq/watch/ui"
    },
    bundle: true,
    format: "esm",
    platform: "browser",
    target: "es2022",
    outdir: process.env.MOQ_VISUALIZER_OUTDIR || "vendor",
    minify: true,
    sourcemap: false
  });
} finally {
  // Keep npm's installed dependency pristine so repeated builds are identical.
  await writeFile(sourcePath, originalSource);
}
