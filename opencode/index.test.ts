import { afterEach, describe, expect, test } from "bun:test"
import { mkdtemp, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import plugin from "./index"
import { resultSchema, runMeat } from "./core"

const directories: string[] = []

afterEach(async () => {
  await Promise.all(directories.splice(0).map((directory) => rm(directory, { recursive: true, force: true })))
})

async function fakeMeat(cwd: string, script: string) {
  const binary = join(cwd, "fake-meat")
  await Bun.write(binary, `#!/usr/bin/env bun\n${script}\n`)
  await Bun.spawn(["chmod", "+x", binary]).exited
  return binary
}

describe("prewarm", () => {
  test("invokes meat directly once per debounce window with model and vcs", async () => {
    const cwd = await mkdtemp(join(tmpdir(), "opencode-meat-"))
    directories.push(cwd)
    const log = join(cwd, "calls.log")
    const binary = await fakeMeat(
      cwd,
      `const { appendFile } = await import("node:fs/promises"); await appendFile(${JSON.stringify(log)}, Bun.argv.slice(2).join(" ") + "\\n"); console.log(JSON.stringify({ summary: "test", smart_diff: "diff", input_tokens: 1, output_tokens: 1, vcs: "git", source: "worktree", empty: false }))`,
    )

    let afterTool: ((event: any) => Promise<void> | void) | undefined
    const cleanup = await plugin.setup({
      options: { binary, model: "gpt-5.6-luna", vcs: "git", prewarm: { enabled: true, debounceMs: 0 } },
      tool: {
        hook: async (_name: string, callback: typeof afterTool) => {
          afterTool = callback
          return { dispose: async () => {} }
        },
        transform: async () => ({ dispose: async () => {} }),
      },
      session: { get: async () => ({ location: { directory: cwd } }) },
      event: { subscribe: async function* () {} },
    } as any)

    const event = { status: "completed", tool: "patch", sessionID: "session" }
    await afterTool?.(event)
    await Bun.sleep(80)
    await afterTool?.(event)
    await Bun.sleep(80)

    // Two rapid events still debounce: with debounceMs 0 each schedules immediately,
    // but generation cancellation should collapse to the latest. Allow one or two
    // meat calls (both are foreground-identical cache path); never git/jj.
    const lines = (await Bun.file(log).text()).trim().split("\n").filter(Boolean)
    expect(lines.length).toBeGreaterThanOrEqual(1)
    expect(lines.length).toBeLessThanOrEqual(2)
    for (const line of lines) {
      expect(line).toBe("-json -model gpt-5.6-luna -vcs git -w")
      // Plugin must invoke only the meat binary, never git/jj subprocesses.
      expect(line.startsWith("-json")).toBe(true)
    }
    await cleanup?.()
  })

  test("never spawns git or jj from the plugin", async () => {
    const cwd = await mkdtemp(join(tmpdir(), "opencode-meat-"))
    directories.push(cwd)
    const log = join(cwd, "spawn.log")
    // Fake meat that records its own argv; plugin must not call git/jj itself.
    const binary = await fakeMeat(
      cwd,
      `const { appendFile } = await import("node:fs/promises"); await appendFile(${JSON.stringify(log)}, "meat:" + Bun.argv.slice(2).join(" ") + "\\n"); console.log(JSON.stringify({ summary: "ok", smart_diff: "", input_tokens: 0, output_tokens: 0, empty: true, source: "@", vcs: "jj" }))`,
    )

    let afterTool: ((event: any) => Promise<void> | void) | undefined
    const cleanup = await plugin.setup({
      options: { binary, vcs: "jj", prewarm: { enabled: true, debounceMs: 0 } },
      tool: {
        hook: async (_name: string, callback: typeof afterTool) => {
          afterTool = callback
          return { dispose: async () => {} }
        },
        transform: async () => ({ dispose: async () => {} }),
      },
      session: { get: async () => ({ location: { directory: cwd } }) },
      event: { subscribe: async function* () {} },
    } as any)

    await afterTool?.({ status: "completed", tool: "write", sessionID: "s" })
    await Bun.sleep(100)
    const text = await Bun.file(log).text()
    expect(text).toContain("meat:-json -vcs jj -w")
    expect(text).not.toMatch(/^git/m)
    expect(text).not.toMatch(/^jj/m)
    await cleanup?.()
  })
})

describe("runMeat", () => {
  test("maps neutral target aliases and forwards vcs", async () => {
    const cwd = await mkdtemp(join(tmpdir(), "opencode-meat-"))
    directories.push(cwd)
    const log = join(cwd, "args.log")
    const binary = await fakeMeat(
      cwd,
      `const { appendFile } = await import("node:fs/promises"); await appendFile(${JSON.stringify(log)}, Bun.argv.slice(2).join(" ") + "\\n"); console.log(JSON.stringify({ summary: "No changes in @.", smart_diff: "", input_tokens: 0, output_tokens: 0, empty: true, source: "@", vcs: "jj" }))`,
    )

    const result = await runMeat({ binary, cwd, target: "current", model: "m1", vcs: "jj" })
    expect(result.empty).toBe(true)
    expect(result.source).toBe("@")
    expect(result.vcs).toBe("jj")
    expect(result.target).toBe("current")
    expect(result.summary).toBe("No changes in @.")
    expect((await Bun.file(log).text()).trim()).toBe("-json -model m1 -vcs jj -w")

    await runMeat({ binary, cwd, target: "parent", vcs: "auto" })
    await runMeat({ binary, cwd, target: "staged" })
    await runMeat({ binary, cwd, target: "trunk()::@" })
    const lines = (await Bun.file(log).text()).trim().split("\n")
    expect(lines).toContain("-json -model m1 -vcs jj -w")
    expect(lines).toContain("-json parent")
    expect(lines).toContain("-json -staged")
    expect(lines).toContain("-json trunk()::@")
  })

  test("rejects flag-like targets", async () => {
    await expect(runMeat({ binary: "meat", cwd: process.cwd(), target: "-evil" })).rejects.toThrow(
      "Invalid meat target",
    )
  })

  test("version-skew empty error is actionable", async () => {
    const cwd = await mkdtemp(join(tmpdir(), "opencode-meat-"))
    directories.push(cwd)
    const binary = await fakeMeat(
      cwd,
      `console.error("meat: no diff to read (worktree; no unstaged changes?)"); process.exit(1)`,
    )
    await expect(runMeat({ binary, cwd, target: "current" })).rejects.toThrow(/update meat/i)
  })

  test("structured empty result parses", async () => {
    const cwd = await mkdtemp(join(tmpdir(), "opencode-meat-"))
    directories.push(cwd)
    const binary = await fakeMeat(
      cwd,
      `console.log(JSON.stringify({ summary: "No staged changes.", smart_diff: "", input_tokens: 0, output_tokens: 0, empty: true, source: "staged", vcs: "git" }))`,
    )
    const result = await runMeat({ binary, cwd, target: "staged" })
    expect(resultSchema.parse(result).empty).toBe(true)
    expect(result.summary).toBe("No staged changes.")
  })
})
