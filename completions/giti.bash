_giti()
{
    local current="${COMP_WORDS[COMP_CWORD]}" first= ref candidate word
    local -a refs
    local positionals=0 after_separator=false index

    while IFS= read -r ref; do
        [[ $ref == */HEAD ]] || refs+=("$ref")
    done < <(git for-each-ref --format='%(refname:short)' refs/heads refs/remotes refs/tags 2>/dev/null)

    for ((index = 1; index < COMP_CWORD; index++)); do
        word=${COMP_WORDS[index]}
        if $after_separator; then
            ((positionals++))
        else
            case $word in
                -f|--foreground|--follow) ;;
                --) after_separator=true ;;
                -1) return ;;
                *)
                    ((positionals++))
                    [[ -n $first ]] || first=$word
                    ;;
            esac
        fi
    done

    COMPREPLY=()
    if ! $after_separator && ((positionals == 0)); then
        for candidate in -f --foreground --follow -1 HEAD "${refs[@]}"; do
            [[ $candidate == "$current"* ]] && COMPREPLY+=("$candidate")
        done
    elif ! $after_separator && { ((positionals != 1)) || ! git rev-parse --verify --quiet "$first^{commit}" >/dev/null; }; then
        return
    fi

    while IFS= read -r candidate; do
        COMPREPLY+=("$candidate")
    done < <(compgen -f -- "$current")
    compopt -o filenames
}

complete -F _giti giti
