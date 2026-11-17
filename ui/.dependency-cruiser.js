/** @type {import('dependency-cruiser').IConfiguration} */
module.exports = {
	forbidden: [
		{
			name: "no-circular",
			severity: "error",
			comment: "Mirrors oxlint import/no-cycle: a cycle means the two modules cannot be reasoned about independently.",
			from: {
				// Pre-existing cycle on origin/dev, untouched by the proxy-pool work; tracked separately.
				pathNot: "^app/workspace/mcp-registry/views/mcpExposeList(\\.tsx|\\.utils\\.ts)$",
			},
			to: { circular: true },
		},
		{
			name: "no-orphans",
			severity: "error",
			comment: "Every module under app/lib/components/hooks is reachable from an entry; dead files should be deleted, not kept.",
			from: {
				path: "^(app|lib|components|hooks)/",
				pathNot: "\\.(test|spec|stories)\\.[jt]sx?$|^app/_fallbacks/enterprise/",
				// dependency-cruiser 18 marks orphans in `from`, not `to.reachable`.
				// Enterprise fallback stubs are loaded by runtime registries, so static analysis
				// cannot see their entry points; exempt that directory explicitly.
				orphan: true,
			},
			to: {},
		},
	],
	options: {
		doNotFollow: {
			path: "node_modules|out|dist",
		},
		tsPreCompilationDeps: true,
		exoticRequireStrings: ["@/.*"],
	},
};