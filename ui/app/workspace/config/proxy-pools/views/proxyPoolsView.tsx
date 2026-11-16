import PageTitle from "@/components/pageTitle";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { getErrorMessage, useGetProxyPoolsQuery } from "@/lib/store";
import { useTranslation } from "react-i18next";

export default function ProxyPoolsView() {
	const { t } = useTranslation("proxypool");
	const { data, isLoading, error } = useGetProxyPoolsQuery();
	const pools = data?.pools ?? [];

	return (
		<div className="mx-auto w-full max-w-5xl space-y-4">
			<PageTitle title={t("title")}>{t("description")}</PageTitle>

			{error != null && (
				<Alert variant="destructive" data-testid="proxy-pool-load-error">
					<AlertDescription>{getErrorMessage(error)}</AlertDescription>
				</Alert>
			)}

			<Table containerClassName="rounded-sm border" data-testid="proxy-pool-table">
				<TableHeader>
					<TableRow>
						<TableHead>{t("columnName")}</TableHead>
						<TableHead>{t("poolDescription")}</TableHead>
						<TableHead>{t("columnProxies")}</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{isLoading ? (
						<TableRow>
							<TableCell colSpan={3} className="text-muted-foreground py-6 text-center">
								{t("loading")}
							</TableCell>
						</TableRow>
					) : pools.length === 0 ? (
						<TableRow>
							<TableCell colSpan={3} className="text-muted-foreground py-6 text-center">
								<div data-testid="proxy-pool-empty">
									<p className="font-medium">{t("noPools")}</p>
									<p className="mt-1 text-sm">{t("noPoolsHint")}</p>
								</div>
							</TableCell>
						</TableRow>
					) : (
						pools.map((pool) => (
							<TableRow key={pool.id} data-testid={`proxy-pool-row-${pool.id}`}>
								<TableCell className="font-medium">{pool.name}</TableCell>
								<TableCell className="text-muted-foreground">{pool.description}</TableCell>
								<TableCell>{pool.proxies.length}</TableCell>
							</TableRow>
						))
					)}
				</TableBody>
			</Table>
		</div>
	);
}
