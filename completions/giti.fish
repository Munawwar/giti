function __giti_refs
    command git for-each-ref --format='%(refname:short)' refs/heads refs/remotes refs/tags 2>/dev/null |
        string match --invert --regex '/HEAD$'
end

function __giti_before_separator
    not contains -- -- (commandline --opc)
end

complete --command giti --short-option f --long-option foreground --description 'Run in the foreground'
complete --command giti --short-option 1 --description 'Restart the resident'
complete --command giti --long-option follow --description 'Follow a file across renames'
complete --command giti --condition __giti_before_separator --arguments '(__giti_refs) HEAD' --description 'Git revision or repository path'
