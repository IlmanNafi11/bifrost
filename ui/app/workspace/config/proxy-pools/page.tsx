import ProxyPoolsView from "./views/proxyPoolsView";

export default function ProxyPoolsPage() {
	return (
		<div className="no-padding-parent flex w-full p-4" data-testid="proxy-pool-nav">
			<ProxyPoolsView />
		</div>
	);
}