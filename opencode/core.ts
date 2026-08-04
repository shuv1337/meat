import { z } from "zod"

export const resultSchema = z.object({
  target: z.string(),
  summary: z.string(),
  smart_diff: z.string(),
  elision: z.string().optional(),
  input_tokens: z.number(),
  output_tokens: z.number(),
  vcs: z.string().optional(),
  source: z.string().optional(),
  empty: z.boolean().optional(),
})

export type MeatResult = z.infer<typeof resultSchema>

export type VcsOption = "auto" | "git" | "jj"

function targetArgs(target: string) {
  switch (target.toLowerCase()) {
    case "":
    case "current":
    case "worktree":
    case "working-tree":
    case "unstaged":
      return ["-w"]
    case "parent":
      return ["parent"]
    case "staged":
    case "index":
      return ["-staged"]
    default:
      if (target.startsWith("-")) throw new Error(`Invalid meat target: ${target}`)
      return [target]
  }
}

export function render(result: MeatResult) {
  const lines = [`# ${result.summary}`]
  if (result.elision) lines.push(`_${result.elision}_`)
  if (result.smart_diff.trim()) lines.push(`\`\`\`diff\n${result.smart_diff.trimEnd()}\n\`\`\``)
  return lines.join("\n\n")
}

export async function runMeat(options: {
  binary: string
  cwd: string
  target: string
  model?: string
  vcs?: VcsOption
  refresh?: boolean
  signal?: AbortSignal
}) {
  const target = options.target.trim()
  const model = options.model?.trim()
  const vcs = options.vcs?.trim()
  const args = [
    "-json",
    ...(model ? ["-model", model] : []),
    ...(vcs && vcs !== "auto" ? ["-vcs", vcs] : []),
    ...(options.refresh ? ["-no-cache"] : []),
    ...targetArgs(target),
  ]

  let process: Bun.Subprocess<"ignore", "pipe", "pipe">
  try {
    process = Bun.spawn([options.binary, ...args], {
      cwd: options.cwd,
      stdout: "pipe",
      stderr: "pipe",
      signal: options.signal,
    })
  } catch (error) {
    throw new Error(
      `Could not start ${options.binary}. Install meat with \`go install meat.dev/cmd/meat@latest\`, or set the plugin's \`binary\` option.`,
      { cause: error },
    )
  }

  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(process.stdout).text(),
    new Response(process.stderr).text(),
    process.exited,
  ])

  if (exitCode !== 0) {
    if (stderr.includes("no diff to read")) {
      throw new Error(
        "No changes to read (or meat is outdated). Update meat with `go install meat.dev/cmd/meat@latest`, then retry.",
      )
    }
    const detail = stderr.trim() || stdout.trim() || `exited with status ${exitCode}`
    throw new Error(`meat failed: ${detail}`)
  }

  let parsed: unknown
  try {
    parsed = JSON.parse(stdout)
  } catch (error) {
    throw new Error("meat returned invalid JSON; update meat with `go install meat.dev/cmd/meat@latest`.", {
      cause: error,
    })
  }
  return resultSchema.parse({ ...(parsed as object), target: target || "current" })
}
