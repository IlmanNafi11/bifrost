import PageTitle from "@/components/pageTitle";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { getErrorMessage, useDeleteProxiesMutation, useDisableProxiesMutation, useProbeProxiesMutation } from "@/lib/store";
import type { ProxyPool, ProxyProbeMode } from "@/lib/types/proxyPools";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import ImportProxiesDialog from "./importProxiesDialog";
import { normaliseProbeMode, proxyStatusTone } from "./proxyPools.utils";

interface Props {
	pool: ProxyPool;
	onChanged: () => void;
}

export default function ProxyPoolDetail({ pool, onChanged }: Props) {
	const { t } = useTranslation("proxypool");
	const [selectedIds, setSelectedIds] = useState<string[]>([]);
	const [probeMode, setProbeMode] = useState<ProxyProbeMode>("a");
	const [importOpen, setImportOpen] = useState(false);
	const [deleteOpen, setDeleteOpen] = useState(false);
	const [probe, { isLoading: isProbing }] = useProbeProxiesMutation();
	const [disable, { isLoading: isDisabling }] = useDisableProxiesMutation();
	const [deleteProxies, { isLoading: isDeleting }] = useDeleteProxiesMutation();

	const toggle = (id: string) =>
		setSelectedIds((current) => (current.includes(id) ? current.filter((value) => value !== id) : [...current, id]));
	const ids = selectedIds.length > 0 ? selectedIds : undefined;

	const tail = (mutation: Promise<unknown>, success: string) =>
		mutation
			.then(() => {
				toast.success(success);
				setSelectedIds([]);
				onChanged();
			})
			.catch((error) => toast.error(t("toastError"), { description: getErrorMessage(error) }));

	return (
		<section className="space-y-4" data-testid={`proxy-pool-detail-${pool.id}`}>
			<div>
				<PageTitle title={pool.name}>{pool.description}</PageTitle>
			</div>

			<div className="flex flex-wrap items-center gap-2">
				<Button size="sm" onClick={() => setImportOpen(true)} data-testid="proxy-pool-import-btn">
					{t("actionImport")}
				</Button>
				<Select value={probeMode} onValueChange={(value) => setProbeMode(normaliseProbeMode(value as ProxyProbeMode))}>
					<SelectTrigger className="w-56" data-testid="proxy-pool-probe-mode-select">
						<SelectValue placeholder={t("probeMode")} />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="a">{t("probeModeA")}</SelectItem>
						<SelectItem value="b">{t("probeModeB")}</SelectItem>
					</SelectContent>
				</Select>
				<Button
					size="sm"
					variant="outline"
					isLoading={isProbing}
					disabled={isProbing}
					onClick={() => tail(probe({ id: pool.id, data: { ids, mode: probeMode } }).unwrap(), t("toastProbed"))}
					data-testid="proxy-pool-probe-btn"
				>
					{t("actionProbe")}
				</Button>
				<Button
					size="sm"
					variant="outline"
					isLoading={isDisabling}
					disabled={selectedIds.length === 0 || isDisabling}
					onClick={() =>
						tail(disable({ id: pool.id, data: { ids: selectedIds } }).unwrap(), t("toastDisabled", { count: selectedIds.length }))
					}
					data-testid="proxy-pool-disable-btn"
				>
					{t("actionDisable")}
				</Button>
				<Button
					size="sm"
					variant="destructive"
					disabled={selectedIds.length === 0}
					onClick={() => setDeleteOpen(true)}
					data-testid="proxy-pool-delete-proxies-btn"
				>
					{t("actionDelete")}
				</Button>
			</div>

			<Table containerClassName="rounded-sm border" data-testid="proxy-pool-proxy-table">
				<TableHeader>
					<TableRow>
						<TableHead className="w-10" />
						<TableHead>{t("columnType")}</TableHead>
						<TableHead>{t("columnUrl")}</TableHead>
						<TableHead>{t("columnWeight")}</TableHead>
						<TableHead>{t("columnStatus")}</TableHead>
						<TableHead>{t("columnLatency")}</TableHead>
						<TableHead>{t("columnEgressIp")}</TableHead>
						<TableHead>{t("statusEnabled")}</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{pool.proxies.length === 0 ? (
						<TableRow>
							<TableCell colSpan={8} className="text-muted-foreground py-6 text-center" data-testid="proxy-pool-proxy-empty">
								{t("noProxies")}
							</TableCell>
						</TableRow>
					) : (
						pool.proxies.map((proxy) => (
							<TableRow key={proxy.id} data-testid={`proxy-pool-proxy-row-${proxy.id}`}>
								<TableCell>
									<Checkbox
										checked={selectedIds.includes(proxy.id)}
										onCheckedChange={() => toggle(proxy.id)}
										aria-label={proxy.url}
										data-testid={`proxy-pool-proxy-checkbox-${proxy.id}`}
									/>
								</TableCell>
								<TableCell>{proxy.type}</TableCell>
								<TableCell className="font-mono text-xs">{proxy.url}</TableCell>
								<TableCell>{proxy.weight}</TableCell>
								<TableCell>
									<Badge variant={proxyStatusTone(proxy.status)} data-testid={`proxy-pool-proxy-status-${proxy.id}`}>
										{proxy.status === "alive" ? t("statusAlive") : proxy.status === "dead" ? t("statusDead") : t("statusUnknown")}
									</Badge>
								</TableCell>
								<TableCell>{proxy.latency_ms > 0 ? `${proxy.latency_ms} ms` : "—"}</TableCell>
								<TableCell className="font-mono text-xs">{proxy.egress_ip || "—"}</TableCell>
								<TableCell>{proxy.enabled ? t("statusEnabled") : t("statusDisabled")}</TableCell>
							</TableRow>
						))
					)}
				</TableBody>
			</Table>

			<ImportProxiesDialog open={importOpen} onOpenChange={setImportOpen} poolId={pool.id} onImported={onChanged} />

			<AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
				<AlertDialogContent data-testid="proxy-pool-delete-proxies-dialog">
					<AlertDialogHeader>
						<AlertDialogTitle>{t("actionDelete")}</AlertDialogTitle>
						<AlertDialogDescription>{t("confirmDeleteProxies")}</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="proxy-pool-delete-proxies-cancel">{t("actionCancel")}</AlertDialogCancel>
						<AlertDialogAction
							data-testid="proxy-pool-delete-proxies-confirm"
							disabled={isDeleting}
							onClick={() =>
								tail(
									deleteProxies({ id: pool.id, data: { ids: selectedIds } }).unwrap(),
									t("toastProxiesDeleted", { count: selectedIds.length }),
								)
							}
						>
							{t("actionDelete")}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</section>
	);
}