import { Plugin } from "@opencode-ai/plugin"
import { z } from "zod"
import { render, resultSchema, runMeat, type VcsOption } from "./core"

function parseVcs(value: unknown): VcsOption | undefined {
  if (value === "auto" || value === "git" || value === "jj") return value
  return undefined
}

export default Plugin.define({
  id: "meat",
  setup: async (ctx) => {
    const binary = typeof ctx.options.binary === "string" ? ctx.options.binary : "meat"
    const model = typeof ctx.options.model === "string" ? ctx.options.model : undefined
    const vcs = parseVcs(ctx.options.vcs)
    const prewarmOptions =
      typeof ctx.options.prewarm === "object" && ctx.options.prewarm !== null ? ctx.options.prewarm : undefined
    const prewarmEnabled = prewarmOptions?.enabled === true
    const debounceMs =
      typeof prewarmOptions?.debounceMs === "number" && prewarmOptions.debounceMs >= 0
        ? prewarmOptions.debounceMs
        : 1500
    const mutatingTools = new Set(["edit", "patch", "write", "shell"])
    const jobs = new Map<
      string,
      {
        timer?: ReturnType<typeof setTimeout>
        controller?: AbortController
        running?: Promise<void>
        generation: number
      }
    >()
    const sessionDirectories = new Map<string, string>()

    const startPrewarm = async (cwd: string, generation: number) => {
      const job = jobs.get(cwd)
      if (!job || job.generation !== generation) return
      if (job.running) await job.running
      if (job.generation !== generation) return

      const controller = new AbortController()
      job.controller = controller
      const running = (async () => {
        try {
          // Delegate diff acquisition, VCS selection, and cache identity to Meat.
          // Unchanged diffs are cache hits; no plugin-side git/jj fingerprint.
          await runMeat({ binary, cwd, target: "current", model, vcs, signal: controller.signal })
        } catch {
          // Prewarming is best-effort; the foreground tool remains the fallback.
        }
      })()
      job.running = running
      await running
      if (job.running === running) job.running = undefined
      if (job.controller === controller) job.controller = undefined
    }

    const schedulePrewarm = (cwd: string, delay: number) => {
      const job = jobs.get(cwd) ?? { generation: 0 }
      if (job.timer) clearTimeout(job.timer)
      job.controller?.abort()
      const generation = ++job.generation
      job.timer = setTimeout(() => {
        job.timer = undefined
        void startPrewarm(cwd, generation)
      }, delay)
      jobs.set(cwd, job)
    }

    const cleanups: Array<() => Promise<void>> = []

    if (prewarmEnabled) {
      const registration = await ctx.tool.hook("execute.after", async (event) => {
        if (event.status !== "completed" || !mutatingTools.has(event.tool)) return
        const session = await ctx.session.get({ sessionID: event.sessionID })
        sessionDirectories.set(event.sessionID, session.location.directory)
        schedulePrewarm(session.location.directory, debounceMs)
      })
      cleanups.push(() => registration.dispose())
    }

    await ctx.tool.transform((tools) => {
      tools.add({
        name: "meat",
        options: { codemode: false },
        description:
          "Create a concise reading diff that preserves the behavior-bearing parts of a change and elides mechanical noise. Use when the user asks to see or run meat. This invokes a model and may take a few minutes; do not call it speculatively during ordinary code review.",
        input: z.object({
          target: z
            .string()
            .default("current")
            .describe(
              'What to read: current (default), parent, staged (Git only), or a Git revision/range or jj revset',
            ),
          refresh: z.boolean().default(false).describe("Ignore meat's cached result and recompute"),
        }),
        output: resultSchema,
        async execute({ target, refresh }, toolCtx) {
          const normalizedTarget = target.trim()
          const session = await ctx.session.get({ sessionID: toolCtx.sessionID })

          await toolCtx.progress({ phase: "abridging", target: normalizedTarget || "current" })
          const result = await runMeat({
            binary,
            cwd: session.location.directory,
            target: normalizedTarget,
            model,
            vcs,
            refresh,
          })
          return { output: result, content: render(result) }
        },
      })
    }).then((registration) => cleanups.push(() => registration.dispose()))

    const eventsController = new AbortController()
    if (prewarmEnabled) {
      void (async () => {
        try {
          for await (const event of ctx.event.subscribe({ signal: eventsController.signal })) {
            if (
              (event.type !== "session.status" || event.data.status.type !== "idle") &&
              event.type !== "session.idle"
            )
              continue
            const cwd = sessionDirectories.get(event.data.sessionID)
            const job = cwd ? jobs.get(cwd) : undefined
            if (cwd && job?.timer) schedulePrewarm(cwd, 0)
          }
        } catch {
          // The event stream ending only disables idle acceleration.
        }
      })()
    }

    return async () => {
      eventsController.abort()
      for (const job of jobs.values()) {
        if (job.timer) clearTimeout(job.timer)
        job.controller?.abort()
      }
      await Promise.all(cleanups.map((cleanup) => cleanup()))
    }
  },
})
