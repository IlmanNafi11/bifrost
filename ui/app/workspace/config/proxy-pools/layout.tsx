import { createFileRoute } from "@tanstack/react-router";
import ProxyPoolsPage from "./page";

export const Route = createFileRoute("/workspace/config/proxy-pools")({
	component: ProxyPoolsPage,
});
