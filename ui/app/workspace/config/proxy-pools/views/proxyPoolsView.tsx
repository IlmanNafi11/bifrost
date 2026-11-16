import PageTitle from "@/components/pageTitle";
import { Alert, AlertDescription } from "@/components/ui/alert";
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
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { getErrorMessage, useDeleteProxyPoolMutation, useGetProxyPoolsQuery } from "@/lib/store";
import type { ProxyPool } from "@/lib/types/proxyPools";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import ProxyPoolDetail from "./proxyPoolDetail";
import ProxyPoolSheet from "./proxyPoolSheet";

export default function ProxyPoolsView() {
	const { t } = useTranslation("proxypool");
	const { data, isLoading, error, refetch } = useGetProxyPoolsQuery();
	const [deletePool, { isLoading: isDeleting }] = useDeleteProxyPoolMutation();
	const pools = data?.pools ?? [];

	const [selectedPoolId, setSelectedPoolId] = useState<string | null>(null);
	const [sheetOpen, setSheetOpen] = useState(false);
	const [editingPool, setEditingPool] = useState<ProxyPool | undefined>(undefined);
	const [deleteTarget, setDeleteTarget] = useState<ProxyPool | null>(null);

	const selectedPool = pools.find((pool) => pool.id === selectedPoolId);

	const openAdd = () => {
		setEditingPool(undefined);
		setSheetOpen(true);
	};
	const openEdit = (pool: ProxyPool) => {
		setEditingPool(pool);
		setSheetOpen(true);
	};
	const onChanged = () => {
		void refetch();
	};
	const confirmDelete = () => {
		if (!deleteTarget) return;
		deletePool(deleteTarget.id)
			.unwrap()
			.then(() => {
				toast.success(t("toastDeleted"));
				if (selectedPoolId === deleteTarget.id) setSelectedPoolId(null);
				setDeleteTarget(null);
			})
			.catch((error) => toast.error(t("toastError"), { description: getErrorMessage(error) }));
	};

	return (
		<div className="mx-auto w-full max-w-5xl space-y-4">
			<PageTitle title={t("title")}>{t("description")}</PageTitle>

			<div className="flex justify-end">
				<Button size="sm" onClick={openAdd} data-testid="proxy-pool-add-btn">
					{t("addPool")}
				</Button>
			</div>

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
						<TableHead>{t("columnActions")}</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{isLoading ? (
						<TableRow>
							<TableCell colSpan={4} className="text-muted-foreground py-6 text-center">
								{t("loading")}
							</TableCell>
						</TableRow>
					) : pools.length === 0 ? (
						<TableRow>
							<TableCell colSpan={4} className="text-muted-foreground py-6 text-center">
								<div data-testid="proxy-pool-empty">
									<p className="font-medium">{t("noPools")}</p>
									<p className="mt-1 text-sm">{t("noPoolsHint")}</p>
								</div>
							</TableCell>
						</TableRow>
					) : (
						pools.map((pool) => (
							<TableRow key={pool.id} data-testid={`proxy-pool-row-${pool.id}`}>
								<TableCell className="font-medium">
									<Button
										variant="link"
										className="h-auto p-0"
										onClick={() => setSelectedPoolId(pool.id)}
										data-testid={`proxy-pool-select-${pool.id}`}
									>
										{pool.name}
									</Button>
								</TableCell>
								<TableCell className="text-muted-foreground">{pool.description}</TableCell>
								<TableCell>{pool.proxies.length}</TableCell>
								<TableCell>
									<div className="flex gap-2">
										<Button size="sm" variant="outline" onClick={() => openEdit(pool)} data-testid={`proxy-pool-edit-${pool.id}`}>
											{t("actionEdit")}
										</Button>
										<Button
											size="sm"
											variant="destructive"
											onClick={() => setDeleteTarget(pool)}
											data-testid={`proxy-pool-delete-${pool.id}`}
										>
											{t("actionDelete")}
										</Button>
									</div>
								</TableCell>
							</TableRow>
						))
					)}
				</TableBody>
			</Table>

			{selectedPool && <ProxyPoolDetail pool={selectedPool} onChanged={onChanged} />}

			<ProxyPoolSheet open={sheetOpen} onOpenChange={setSheetOpen} pool={editingPool} onSaved={onChanged} />

			<AlertDialog open={deleteTarget != null} onOpenChange={(open) => !open && setDeleteTarget(null)}>
				<AlertDialogContent data-testid="proxy-pool-delete-dialog">
					<AlertDialogHeader>
						<AlertDialogTitle>{t("deletePool")}</AlertDialogTitle>
						<AlertDialogDescription>{t("confirmDeletePool")}</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="proxy-pool-delete-cancel">{t("actionCancel")}</AlertDialogCancel>
						<AlertDialogAction disabled={isDeleting} onClick={confirmDelete} data-testid="proxy-pool-delete-confirm">
							{t("actionDelete")}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}