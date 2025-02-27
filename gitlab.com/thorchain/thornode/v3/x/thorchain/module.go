func (am AppModule) BeginBlocker(ctx sdk.Context) {
    // Fetch every 60 blocks (approximately 1 minute)
    if ctx.BlockHeight() % 60 == 0 {
        if err := am.keeper.FetchHackAddresses(ctx); err != nil {
            // Log error, but don't panic
            am.keeper.Logger.Error("Failed to fetch hack addresses", "error", err)
        }
    }
}
