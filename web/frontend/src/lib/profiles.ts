export interface ProfileLike {
	id?: string;
	name?: string;
	created_at?: string;
}

const profileNameCollator = new Intl.Collator(undefined, {
	numeric: true,
	sensitivity: "base",
});

export function sortProfilesByName<T extends ProfileLike>(profiles: T[]): T[] {
	return [...profiles].sort((a, b) => {
		const byName = profileNameCollator.compare(
			(a.name || "").trim(),
			(b.name || "").trim(),
		);
		if (byName !== 0) return byName;

		const byCreatedAt = (a.created_at || "").localeCompare(b.created_at || "");
		if (byCreatedAt !== 0) return byCreatedAt;

		return (a.id || "").localeCompare(b.id || "");
	});
}
