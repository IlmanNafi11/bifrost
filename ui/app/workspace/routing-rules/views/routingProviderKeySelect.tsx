/** Provider key picker for a routing target or fallback. Keys are scoped to one provider. */

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { X } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";

interface ProviderKeySelectProps {
	idPrefix: string;
	clearLabel: string;
	provider?: string;
	keyId?: string;
	allKeys: Array<{ key_id: string; name: string; provider: string }>;
	onChange: (keyId: string) => void;
}

/** Renders nothing until a provider is chosen, since keys are scoped to one. */
export function ProviderKeySelect({ idPrefix, clearLabel, provider, keyId, allKeys, onChange }: ProviderKeySelectProps) {
	const { t } = useTranslation("models");
	const availableKeys = provider ? allKeys.filter((k) => k.provider === provider).map((k) => ({ id: k.key_id, name: k.name })) : [];
	if (!provider || (availableKeys.length === 0 && !keyId)) {
		return null;
	}

	return (
		<div className="space-y-1.5">
			<Label id={`${idPrefix}-apikey-label`} className="text-xs">
				<Trans i18nKey="routing.apiKeyOptional" ns="models" components={{ muted: <span className="text-muted-foreground" /> }} />
			</Label>
			<div className="flex gap-1.5">
				<Select value={keyId || ""} onValueChange={onChange}>
					<SelectTrigger
						id={`${idPrefix}-apikey-select`}
						aria-labelledby={`${idPrefix}-apikey-label`}
						className="h-9 flex-1 text-sm"
						data-testid={`${idPrefix}-apikey-select`}
					>
						<SelectValue placeholder={t("routing.selectKeyOptional")} />
					</SelectTrigger>
					<SelectContent>
						{availableKeys.map((key) => (
							<SelectItem key={key.id} value={key.id}>
								{key.name}
							</SelectItem>
						))}
						{keyId && !availableKeys.some((k) => k.id === keyId) && (
							<SelectItem key={`pinned-${keyId}`} value={keyId}>
								{t("routing.pinnedKey", { id: keyId })}
							</SelectItem>
						)}
					</SelectContent>
				</Select>
				{keyId && (
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => onChange("")}
						className="h-9 w-9 p-0"
						aria-label={clearLabel}
						data-testid={`${idPrefix}-apikey-clear`}
					>
						<X className="h-3.5 w-3.5" />
					</Button>
				)}
			</div>
		</div>
	);
}