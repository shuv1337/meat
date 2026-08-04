import { Plugin } from "@opencode-ai/plugin/tui"
import { render, runMeat, type VcsOption } from "./core"

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error)
}

function parseVcs(value: unknown): VcsOption | undefined {
  if (value === "auto" || value === "git" || value === "jj") return value
  return undefined
}

export default Plugin.define({
  id: "meat.tui",
  setup(context) {
    const binary = typeof context.options.binary === "string" ? context.options.binary : "meat"
    const model = typeof context.options.model === "string" ? context.options.model : undefined
    const vcs = parseVcs(context.options.vcs)

    context.ui.slot("app", () => {
      context.keymap.layer(() => ({
        mode: "global",
        commands: [
          {
            id: "meat.run",
            title: "Show a meat reading diff",
            description:
              "Run meat directly [current|parent|staged (Git only)|Git revision/range or jj revset]",
            slash: { name: "meat", arguments: true },
            async run(input) {
              const route = context.ui.router.current()
              if (route.type !== "session") {
                context.ui.toast.show({ message: "Open a session before running /meat", variant: "error" })
                return
              }

              const session = context.data.session.get(route.sessionID)
              if (!session) {
                context.ui.toast.show({ message: "The active session is not available", variant: "error" })
                return
              }

              const target = input?.trim() ?? ""
              const label = target || "current"
              context.ui.toast.show({ message: `Running meat for ${label}...`, variant: "info" })
              try {
                const result = await runMeat({
                  binary,
                  cwd: session.location.directory,
                  target,
                  model,
                  vcs,
                })
                const display = result.source || result.target
                const metadata: Record<string, string> = {
                  source: "meat",
                  target: result.target,
                }
                if (result.vcs) metadata.vcs = result.vcs
                if (result.source) metadata.meatSource = result.source
                await context.client.session.synthetic({
                  sessionID: route.sessionID,
                  text: render(result),
                  description: `meat ${display}`,
                  metadata,
                  resume: false,
                })
              } catch (error) {
                context.ui.toast.show({ title: "meat failed", message: errorMessage(error), variant: "error" })
              }
            },
          },
        ],
      }))
      return null
    })
  },
})
